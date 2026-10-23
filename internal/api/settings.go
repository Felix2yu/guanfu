package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/Felix2yu/guanfu/internal/config"
	"github.com/Felix2yu/guanfu/internal/settings"
	"github.com/Felix2yu/guanfu/internal/sync"
)

// maxSettingsBody 配置请求体上限：这几个接口收的都是短字段，1MB 足够，
// 别给"往磁盘写超大 JSON"留口子。
const maxSettingsBody = 1 << 20

// decodeBody 宽松解码：空 body 视为零值（探测/测试接口允许不带草稿）。
func decodeBody(r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	err := json.NewDecoder(io.LimitReader(r.Body, maxSettingsBody)).Decode(dst)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// handleSettings 一次返回设置页要的全部内容。
//
// 注意：凭据只有掩码（SecretHint），任何分支都不会下发明文——
// 一旦下发，浏览器的 devtools / 代理日志 / 截图都会成为泄漏面。
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"code": 0,
		"data": map[string]any{
			"endpoints":     s.settings.List(),
			"policy":        s.settings.Policy(),
			"secret_source": s.settings.SecretSource(),
			// 认证方式清单由后端给，前端不抄一份常量（抄了就会漂移）。
			"auth_modes": config.AuthModes,
		},
	})
}

// handleSaveEndpoint PUT /api/v1/settings/endpoints/{name}
func (s *Server) handleSaveEndpoint(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var in settings.Input
	if err := decodeBody(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	v, err := s.settings.Save(name, in)
	if err != nil {
		writeSettingsErr(w, err)
		return
	}
	// 保存只同步被改动的这一个源：Reconfigure 重建整批调度时不再追加同步，
	// 否则改一个源等于把全部源都打一遍（页面看起来就像"点了全局同步"）。
	// 未启用/未注册的源名 Trigger 会拒收，这里不必当错误——保存本身已经成功。
	if v.Kind == "source" {
		s.noteTrigger(name, s.engine.Trigger(name))
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": v})
}

// handleResetEndpoint DELETE /api/v1/settings/endpoints/{name}
func (s *Server) handleResetEndpoint(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	removed, err := s.settings.Reset(name)
	if err != nil {
		writeSettingsErr(w, err)
		return
	}
	v, ok := s.settings.View(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "未知端点: "+name)
		return
	}
	// 删掉页面配置后回落到环境变量那份，同样只同步这一个源。
	if v.Kind == "source" {
		s.noteTrigger(name, s.engine.Trigger(name))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code": 0,
		"data": map[string]any{"removed": removed, "endpoint": v},
	})
}

// noteTrigger 处理「配置写成功之后顺带触发同步」的结果：只有队列满值得记一行，
// 未知源名与未启用都是正常分支（禁用中的源本来就不该被同步）。
func (s *Server) noteTrigger(name string, res sync.TriggerResult) {
	if res == sync.TriggerBusy {
		log.Printf("[api] 保存 %s 后没有触发同步：触发队列已满", name)
	}
}

// handleVerifyEndpoint POST /api/v1/settings/endpoints/{name}/verify
//
// 探测失败不算 API 错误：HTTP 200 + ok:false + message，
// 前端照常渲染结论，不必把可预期的「认证被拒」当成异常分支处理。
func (s *Server) handleVerifyEndpoint(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var draft settings.Input
	if err := decodeBody(r, &draft); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	res, err := s.settings.ProbeSource(r.Context(), name, &draft)
	if err != nil {
		writeSettingsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": res})
}

// handleTestNotify POST /api/v1/settings/notify/{name}/test
//
// 同样把「发送失败」当成正常结果返回（200 + ok:false），
// 让页面能显示上游状态码与响应体——这正是排查渠道配置最需要的东西。
func (s *Server) handleTestNotify(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	res, err := s.settings.TestNotify(r.Context(), name)
	if err != nil {
		if errors.Is(err, settings.ErrUnknownEndpoint) {
			writeErr(w, http.StatusNotFound, "未知通知渠道: "+name)
			return
		}
		// 渠道已配置但上游失败：把真实返回带上。
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": res})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": res})
}

// handleSavePolicy PUT /api/v1/settings/policy
func (s *Server) handleSavePolicy(w http.ResponseWriter, r *http.Request) {
	var p settings.Policy
	if err := decodeBody(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if err := s.settings.SetPolicy(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": s.settings.Policy()})
}

// writeSettingsErr 把配置层的错误分派成合适的状态码：
// 未知端点 404（客户端拼错了名字），其余 400（输入不合法）。
func writeSettingsErr(w http.ResponseWriter, err error) {
	if errors.Is(err, settings.ErrUnknownEndpoint) {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeErr(w, http.StatusBadRequest, err.Error())
}
