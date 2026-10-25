package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"derp-orch/internal/agent"
	"derp-orch/internal/store"
)

var (
	slugPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	domainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

	errRegionDisabled = errors.New("地区已停用")
	errRegionUnknown  = errors.New("地区不存在")
)

// localSlug 本地地区 slug(配置为空时用 "local")。
func (s *Server) localSlug() string {
	if s.cfg.RegionSlug != "" {
		return s.cfg.RegionSlug
	}
	return "local"
}

// regionFor 解析地区。空 slug 表示本地地区。
// 老库/测试环境尚未 seed 地区表时,允许从配置合成本地地区。
func (s *Server) regionFor(ctx context.Context, slug string) (*store.Region, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		slug = s.localSlug()
	}
	reg, err := s.store.GetRegion(ctx, slug)
	if err == nil {
		if !reg.Enabled {
			return nil, errRegionDisabled
		}
		return reg, nil
	}
	if !errors.Is(err, store.ErrRegionNotFound) {
		return nil, err
	}
	if slug != s.localSlug() || s.cfg.RelayBaseDomain == "" {
		return nil, errRegionUnknown
	}
	return &store.Region{
		ID:         slug,
		Name:       firstNonEmpty(s.cfg.RegionName, slug),
		BaseDomain: s.cfg.RelayBaseDomain,
		RegionCode: firstNonEmpty(s.cfg.RegionCode, slug),
		Enabled:    true,
	}, nil
}

// regionView 公开地区视图(不泄露代理地址与令牌)。
type regionView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	BaseDomain string `json:"baseDomain"`
	RegionCode string `json:"regionCode"`
}

// handlePublicRegions GET /v1/regions。兑换页地区选择用。
func (s *Server) handlePublicRegions(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListRegions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]regionView, 0, len(list))
	for _, reg := range list {
		if !reg.Enabled {
			continue
		}
		out = append(out, regionView{ID: reg.ID, Name: reg.Name, BaseDomain: reg.BaseDomain, RegionCode: reg.RegionCode})
	}
	// 空库(尚未 seed)时至少返回本地地区,兑换页不至于没有选项。
	if len(out) == 0 {
		if reg, err := s.regionFor(r.Context(), ""); err == nil {
			out = append(out, regionView{ID: reg.ID, Name: reg.Name, BaseDomain: reg.BaseDomain, RegionCode: reg.RegionCode})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"regions": out})
}

// adminRegionView 管理视图:附带令牌是否已配置与实例数。
type adminRegionView struct {
	*store.Region
	HasAgentToken bool `json:"hasAgentToken"`
	Local         bool `json:"local"`
	InstanceCount int  `json:"instanceCount"`
}

func (s *Server) adminRegionViews(ctx context.Context, list []*store.Region) []adminRegionView {
	out := make([]adminRegionView, 0, len(list))
	for _, reg := range list {
		n, err := s.store.CountInstancesByRegion(ctx, reg.ID)
		if err != nil {
			n = 0
		}
		out = append(out, adminRegionView{
			Region:        reg,
			HasAgentToken: reg.AgentToken != "",
			Local:         reg.Local(),
			InstanceCount: n,
		})
	}
	return out
}

// handleAdminRegions GET /v1/admin/regions。
func (s *Server) handleAdminRegions(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListRegions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"regions": s.adminRegionViews(r.Context(), list)})
}

// regionRequest 新建/修改地区请求。修改时 id 忽略,agentToken 为空表示保留原值。
type regionRequest struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	BaseDomain    string `json:"baseDomain"`
	RegionCode    string `json:"regionCode"`
	AgentURL      string `json:"agentUrl"`
	AgentToken    string `json:"agentToken"`
	MaxContainers int    `json:"maxContainers"`
	Enabled       *bool  `json:"enabled"`
}

// validate 校验通用字段。
func (req *regionRequest) validate(isLocal bool) error {
	req.Name = strings.TrimSpace(req.Name)
	req.BaseDomain = strings.ToLower(strings.TrimSpace(req.BaseDomain))
	req.RegionCode = strings.ToLower(strings.TrimSpace(req.RegionCode))
	req.AgentURL = strings.TrimRight(strings.TrimSpace(req.AgentURL), "/")
	if req.Name == "" {
		return errors.New("地区名称必填")
	}
	if !domainPattern.MatchString(req.BaseDomain) {
		return errors.New("基础域名格式不合法(例如 cd.easylan.site)")
	}
	if req.RegionCode == "" || len(req.RegionCode) > 32 {
		return errors.New("regionCode 必填且不超过 32 字符")
	}
	if req.MaxContainers < 0 || req.MaxContainers > 10000 {
		return errors.New("maxContainers 必须在 0-10000 之间(0 表示沿用平台默认)")
	}
	if isLocal {
		if req.AgentURL != "" {
			return errors.New("本地地区不能配置代理地址")
		}
		return nil
	}
	u, err := url.Parse(req.AgentURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errors.New("代理地址必须是 http(s):// 开头的完整 URL")
	}
	return nil
}

// handleAdminCreateRegion POST /v1/admin/regions。
func (s *Server) handleAdminCreateRegion(w http.ResponseWriter, r *http.Request) {
	var req regionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体必须是 JSON")
		return
	}
	id := strings.ToLower(strings.TrimSpace(req.ID))
	if id == "" {
		writeError(w, http.StatusBadRequest, "缺少地区 id")
		return
	}
	if !slugPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "地区 id 只能是小写字母/数字/中划线,1-32 位")
		return
	}
	if id == s.localSlug() {
		writeError(w, http.StatusConflict, "本地地区已存在,请直接修改")
		return
	}
	if err := req.validate(false); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.AgentToken == "" {
		writeError(w, http.StatusBadRequest, "远程地区必须填写代理令牌")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	reg := &store.Region{
		ID:            id,
		Name:          req.Name,
		BaseDomain:    req.BaseDomain,
		RegionCode:    req.RegionCode,
		AgentURL:      req.AgentURL,
		AgentToken:    req.AgentToken,
		MaxContainers: req.MaxContainers,
		Enabled:       enabled,
		CreatedAt:     time.Now(),
	}
	if err := s.store.CreateRegion(r.Context(), reg); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "地区 id 已存在")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.store.AddAudit(r.Context(), store.AuditLog{Kind: "create_region", Detail: fmt.Sprintf("新增地区 %s(%s) 域名=%s 代理=%s", reg.ID, reg.Name, reg.BaseDomain, firstNonEmpty(reg.AgentURL, "本机"))})
	writeJSON(w, http.StatusCreated, adminRegionView{Region: reg, HasAgentToken: true, InstanceCount: 0})
}

// handleAdminUpdateRegion PUT /v1/admin/regions/{id}。
func (s *Server) handleAdminUpdateRegion(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	reg, err := s.store.GetRegion(r.Context(), id)
	if errors.Is(err, store.ErrRegionNotFound) {
		writeError(w, http.StatusNotFound, "地区不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var req regionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体必须是 JSON")
		return
	}
	isLocal := reg.Local() && id == s.localSlug()
	if err := req.validate(isLocal); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// 已有实例时禁止改基础域名:实例子域名已按旧域名签发,改域名会让存量中继失联。
	if req.BaseDomain != reg.BaseDomain {
		if n, err := s.store.CountInstancesByRegion(r.Context(), id); err == nil && n > 0 {
			writeError(w, http.StatusBadRequest, "该地区仍有实例,不能修改基础域名")
			return
		}
	}
	reg.Name = req.Name
	reg.BaseDomain = req.BaseDomain
	reg.RegionCode = req.RegionCode
	reg.MaxContainers = req.MaxContainers
	if !isLocal {
		reg.AgentURL = req.AgentURL
		if req.AgentToken != "" {
			reg.AgentToken = req.AgentToken
		}
		if reg.AgentToken == "" {
			writeError(w, http.StatusBadRequest, "远程地区必须配置代理令牌")
			return
		}
	}
	if req.Enabled != nil {
		reg.Enabled = *req.Enabled
	}
	if err := s.store.UpdateRegion(r.Context(), reg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.store.AddAudit(r.Context(), store.AuditLog{Kind: "update_region", Detail: fmt.Sprintf("修改地区 %s(%s) 域名=%s 启用=%v", reg.ID, reg.Name, reg.BaseDomain, reg.Enabled)})
	writeJSON(w, http.StatusOK, adminRegionView{Region: reg, HasAgentToken: reg.AgentToken != "", Local: reg.Local()})
}

// handleAdminDeleteRegion DELETE /v1/admin/regions/{id}。
func (s *Server) handleAdminDeleteRegion(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	if reg, err := s.store.GetRegion(r.Context(), id); err == nil && reg.Local() {
		writeError(w, http.StatusBadRequest, "不能删除本地地区")
		return
	}
	err := s.store.DeleteRegion(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrRegionNotFound):
		writeError(w, http.StatusNotFound, "地区不存在")
		return
	case errors.Is(err, store.ErrRegionInUse):
		writeError(w, http.StatusConflict, "该地区仍有实例,请先删除实例")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.store.AddAudit(r.Context(), store.AuditLog{Kind: "delete_region", Detail: "删除地区 " + id})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// agentInfoer 由 internal/region.Router 实现。
type agentInfoer interface {
	AgentInfo(ctx context.Context, slug string) (*agent.InfoResponse, error)
}

// handleAdminTestRegion POST /v1/admin/regions/{id}/test。
func (s *Server) handleAdminTestRegion(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	if _, err := s.store.GetRegion(r.Context(), id); errors.Is(err, store.ErrRegionNotFound) {
		writeError(w, http.StatusNotFound, "地区不存在")
		return
	}
	probe, ok := s.orch.(agentInfoer)
	if !ok {
		writeError(w, http.StatusNotImplemented, "当前编排器不支持地区探测")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	info, err := probe.AgentInfo(ctx, id)
	if err != nil {
		writeError(w, http.StatusBadGateway, "探测失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"info": info})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
