package vm2api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Slot 是面板槽位的白名单视图，不包含邮箱、账号 ID、代理密码或 OAuth 凭据。
type Slot struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	ExitID   string `json:"exit_id"`
	ExitName string `json:"exit_name"`
	HasAuth  bool   `json:"has_auth"`
	Ready    bool   `json:"ready"`
	runtime  string
	nodeID   string
}

// Exit 只展示已配置出口的状态，不公开 SOCKS 地址及认证参数。
type Exit struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Available bool   `json:"available"`
}

// CreateSlotRequest 的 ID 必须由持久化开户作业生成，不能使用管理员输入的任意槽位 ID。
type CreateSlotRequest struct {
	ID     string
	Name   string
	ExitID string
}

// PoolKey 仅供本地渠道创建使用；JSON 永远不输出上游明文 Key。
type PoolKey struct {
	PoolID string `json:"pool_id"`
	KeyID  string `json:"key_id"`
	Key    string `json:"-"`
}

type upstreamSlot struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	Family          string `json:"family"`
	Codex           bool   `json:"codex_kernel"`
	HasToken        bool   `json:"has_token"`
	Schedulable     bool   `json:"schedulable"`
	Runtime         string `json:"runtime_type"`
	NodeID          string `json:"node_id"`
	ProxyID         string `json:"proxy_id"`
	ProxyCLIEnabled bool   `json:"proxy_cli_enabled"`
	Proxy           struct {
		ID     string `json:"id"`
		Label  string `json:"label"`
		Scheme string `json:"scheme"`
	} `json:"proxy"`
}

// SlotConnection 只描述槽位配置的出口，不含地址、认证参数，也不代表实测公网 IP 或连通性。
type SlotConnection struct {
	Status   string `json:"status"`
	SlotName string `json:"slot_name,omitempty"`
	ExitID   string `json:"exit_id,omitempty"`
	ExitName string `json:"exit_name,omitempty"`
	ExitType string `json:"exit_type,omitempty"`
}

// SlotConnections 批量读取本机 GPT/Claude 槽位的配置连接，供管理员按实例及槽位精确关联。
// ctx 限制两次只读面板请求的总时长；任一步失败返回错误，不能将未知出口当成未配置。
func (c *Client) SlotConnections(ctx context.Context) (map[string]SlotConnection, error) {
	var response struct {
		Data struct {
			Items []upstreamSlot `json:"items"`
		} `json:"data"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/panel/vms", nil, &response); err != nil {
		return nil, err
	}
	exits, err := c.ListExits(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Exit, len(exits))
	for _, exit := range exits {
		byID[exit.ID] = exit
	}
	result := make(map[string]SlotConnection, len(response.Data.Items))
	for _, raw := range response.Data.Items {
		slot := raw.safeView()
		// 同名槽位可能存在于历史集群节点；只接受当前独立实例的本机 GPT/Claude 槽位。
		if slot.nodeID != "" || (slot.Kind != "codex" && slot.Kind != "claude") || !identifier.MatchString(slot.ID) {
			continue
		}
		connection := SlotConnection{Status: "unconfigured", SlotName: slot.Name}
		if slot.ExitID != "" {
			connection.Status, connection.ExitID = "configured", slot.ExitID
			connection.ExitName, connection.ExitType = slot.ExitName, raw.Proxy.Scheme
			if exit, ok := byID[slot.ExitID]; ok {
				connection.ExitName, connection.ExitType = exit.Name, exit.Type
			}
			// 已绑定的出口可能因容量已满而不可再分配；ListExits.Available 不是此账号的连接状态。
			// 实际是否启用代理以槽位开关为准，不能只因仍保存 proxy_id 就宣称正在使用。
			if !raw.ProxyCLIEnabled {
				connection.Status = "disabled"
			}
		}
		result[slot.ID] = connection
	}
	return result, nil
}

// safeView 显式构建返回字段，防止面板以后新增敏感字段时被自动转发。
func (s upstreamSlot) safeView() Slot {
	kind := s.Family
	if s.Codex {
		kind = "codex"
	}
	exitID := s.ProxyID
	if exitID == "" {
		exitID = s.Proxy.ID
	}
	return Slot{ID: s.ID, Name: s.Name, Kind: kind, Status: s.Status, ExitID: exitID, ExitName: s.Proxy.Label, HasAuth: s.HasToken, Ready: s.HasToken && s.Status == "running" && s.Schedulable && s.NodeID == "", runtime: s.Runtime, nodeID: s.NodeID}
}

// ListSlots 获取独立实例本机的 Codex 槽位，不把历史集群节点混入开户目标。
func (c *Client) ListSlots(ctx context.Context) ([]Slot, error) {
	var response struct {
		Data struct {
			Items []upstreamSlot `json:"items"`
		} `json:"data"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/panel/vms", nil, &response); err != nil {
		return nil, err
	}
	result := make([]Slot, 0, len(response.Data.Items))
	for _, raw := range response.Data.Items {
		slot := raw.safeView()
		if slot.Kind == "codex" && slot.nodeID == "" {
			result = append(result, slot)
		}
	}
	return result, nil
}

// GetSlot 读取指定本地槽位；调用方必须另外校验本地作业归属及账号绑定关系。
func (c *Client) GetSlot(ctx context.Context, slotID string) (Slot, error) {
	if !identifier.MatchString(slotID) {
		return Slot{}, &Error{Code: "invalid_slot", HTTPStatus: 400}
	}
	var response struct {
		Data struct {
			VM upstreamSlot `json:"vm"`
		} `json:"data"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/panel/vms/"+slotID, nil, &response); err != nil {
		return Slot{}, err
	}
	slot := response.Data.VM.safeView()
	if slot.ID != slotID || slot.nodeID != "" || slot.Kind != "codex" {
		return Slot{}, &Error{Code: "unsupported_slot", HTTPStatus: 409}
	}
	return slot, nil
}

// ListExits 仅返回已登记的本机可用出口；不会创建 IPv6 地址或猜测代理配置。
func (c *Client) ListExits(ctx context.Context) ([]Exit, error) {
	var response struct {
		Data struct {
			Proxies []struct {
				ID            string `json:"id"`
				Label         string `json:"label"`
				Kind          string `json:"kind"`
				NodeID        string `json:"node_id"`
				Enabled       bool   `json:"enabled"`
				Status        string `json:"status"`
				BlockedReason string `json:"blocked_reason"`
				BoundCount    int    `json:"bound_count"`
				BindLimit     int    `json:"bind_limit"`
			} `json:"proxies"`
		} `json:"data"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/panel/proxies", nil, &response); err != nil {
		return nil, err
	}
	result := make([]Exit, 0, len(response.Data.Proxies))
	for _, p := range response.Data.Proxies {
		if p.NodeID != "" || !identifier.MatchString(p.ID) {
			continue
		}
		available := p.Enabled && p.Status != "dead" && p.Status != "fail" && p.BlockedReason == "" && p.BoundCount < max(1, p.BindLimit)
		name := p.Label
		if name == "" {
			name = p.ID
		}
		result = append(result, Exit{ID: p.ID, Name: name, Type: p.Kind, Available: available})
	}
	return result, nil
}

// CreateSlot 创建固定规格本地 Docker/Codex 槽位。重试仅恢复同一已记录 ID，调用方必须持有该作业租约。
func (c *Client) CreateSlot(ctx context.Context, input CreateSlotRequest) (Slot, error) {
	if !managedIdentifier.MatchString(input.ID) || strings.TrimSpace(input.Name) == "" || !utf8.ValidString(input.Name) || utf8.RuneCountInString(input.Name) > 60 || strings.ContainsAny(input.Name, "\r\n\x00") || !identifier.MatchString(input.ExitID) {
		return Slot{}, &Error{Code: "invalid_slot_request", HTTPStatus: 400}
	}
	existing, err := c.GetSlot(ctx, input.ID)
	if err == nil {
		if existing.Name != input.Name || existing.ExitID != input.ExitID || existing.runtime != "docker" {
			return Slot{}, &Error{Code: "slot_identity_conflict", HTTPStatus: 409}
		}
		return existing, nil
	}
	var upstreamErr *Error
	if !errors.As(err, &upstreamErr) || upstreamErr.HTTPStatus != 404 {
		return Slot{}, err
	}
	exits, err := c.ListExits(ctx)
	if err != nil {
		return Slot{}, err
	}
	available := false
	for _, exit := range exits {
		if exit.ID == input.ExitID && exit.Available {
			available = true
			break
		}
	}
	if !available {
		return Slot{}, &Error{Code: "exit_unavailable", HTTPStatus: 409}
	}
	// 不接收 runtime、node_id 或 machine 等浏览器参数，避免开户绕过本机及资源规格约束。
	body := map[string]any{"id": input.ID, "name": input.Name, "proxy_id": input.ExitID, "runtime_type": "docker", "kind": "codex", "platform": "openai", "family": "codex", "start": true, "machine": map[string]any{"memory": "512m"}}
	var response struct {
		Data struct {
			VM upstreamSlot `json:"vm"`
		} `json:"data"`
	}
	createErr := c.request(ctx, http.MethodPost, "/api/panel/vms/create", body, &response)
	if createErr == nil {
		slot := response.Data.VM.safeView()
		if slot.ID == input.ID && slot.Kind == "codex" && slot.nodeID == "" {
			return slot, nil
		}
		return Slot{}, &Error{Code: "invalid_created_slot", HTTPStatus: 502}
	}
	// 写请求超时可能已经落盘，只读找回后返回；绝不在同一次失败中创建另一个槽位。
	existing, err = c.GetSlot(ctx, input.ID)
	if err == nil && existing.Name == input.Name && existing.ExitID == input.ExitID && existing.runtime == "docker" {
		return existing, nil
	}
	return Slot{}, createErr
}

type upstreamPool struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	VMIDs   []string `json:"vm_ids"`
}
type upstreamKey struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	PoolID string `json:"vm_pool_id"`
}

// findPool 优先找回稳定名称，也可复用唯一的既有单槽池；不改池名、成员或旧 Key。
func (c *Client) findPool(ctx context.Context, slotID, name string) (*upstreamPool, error) {
	var response struct {
		Data struct {
			Pools []upstreamPool `json:"pools"`
		} `json:"data"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/panel/vm-pools", nil, &response); err != nil {
		return nil, err
	}
	var found *upstreamPool
	for _, pool := range response.Data.Pools {
		containsSlot := false
		for _, member := range pool.VMIDs {
			if member == slotID {
				containsSlot = true
				break
			}
		}
		if pool.Name != name && !containsSlot {
			continue
		}
		// 已有独占池可接入新门户，但不把共享池误当作独占账号；旧 Key 的有效性保持不变。
		if found != nil || !identifier.MatchString(pool.ID) || !pool.Enabled || len(pool.VMIDs) != 1 || pool.VMIDs[0] != slotID {
			return nil, &Error{Code: "pool_scope_conflict", HTTPStatus: 409}
		}
		copy := pool
		found = &copy
	}
	return found, nil
}

// findKey 拒绝同名多 Key 和任何错池或停用 Key，防止重试关联到他人账号。
func (c *Client) findKey(ctx context.Context, poolID, name string) (*upstreamKey, error) {
	var response struct {
		Keys []upstreamKey `json:"keys"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/panel/api-keys", nil, &response); err != nil {
		return nil, err
	}
	var found *upstreamKey
	for _, key := range response.Keys {
		if key.Name != name {
			continue
		}
		if found != nil || !identifier.MatchString(key.ID) || key.PoolID != poolID || key.Status != "active" {
			return nil, &Error{Code: "key_scope_conflict", HTTPStatus: 409}
		}
		copy := key
		found = &copy
	}
	return found, nil
}

// EnsurePoolKey 为持有租约的开户作业建立或复用单槽位池并创建专用 Key；重试按稳定名称找回。
// 调用方必须排除既有门户绑定；复用历史独占池时保留旧 Key，不代表撤销旧客户端访问。
func (c *Client) EnsurePoolKey(ctx context.Context, slotID, stableName string) (PoolKey, error) {
	if !identifier.MatchString(slotID) || !managedIdentifier.MatchString(stableName) {
		return PoolKey{}, &Error{Code: "invalid_pool_request", HTTPStatus: 400}
	}
	slot, err := c.GetSlot(ctx, slotID)
	if err != nil {
		return PoolKey{}, err
	}
	if !slot.HasAuth || !slot.Ready {
		return PoolKey{}, &Error{Code: "slot_not_ready", HTTPStatus: 409}
	}
	pool, err := c.findPool(ctx, slotID, stableName)
	if err != nil {
		return PoolKey{}, err
	}
	if pool == nil {
		createErr := c.request(ctx, http.MethodPost, "/api/panel/vm-pools", map[string]any{"name": stableName, "vm_ids": []string{slotID}, "enabled": true}, nil)
		pool, err = c.findPool(ctx, slotID, stableName)
		if err != nil {
			return PoolKey{}, err
		}
		if pool == nil {
			if createErr != nil {
				return PoolKey{}, createErr
			}
			return PoolKey{}, &Error{Code: "pool_creation_unconfirmed", HTTPStatus: 502}
		}
	}
	key, err := c.findKey(ctx, pool.ID, stableName)
	if err != nil {
		return PoolKey{}, err
	}
	if key == nil {
		createErr := c.request(ctx, http.MethodPost, "/api/panel/api-keys", map[string]any{"name": stableName, "vm_pool_id": pool.ID, "category": "oauth", "max_concurrency": 2}, nil)
		key, err = c.findKey(ctx, pool.ID, stableName)
		if err != nil {
			return PoolKey{}, err
		}
		if key == nil {
			if createErr != nil {
				return PoolKey{}, createErr
			}
			return PoolKey{}, &Error{Code: "key_creation_unconfirmed", HTTPStatus: 502}
		}
	}
	// Reveal 只到后端，单独使用不可 JSON 输出的字段承接明文，防止序列化整个结果时误泄露。
	var reveal struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := c.request(ctx, http.MethodPost, "/api/panel/api-keys/"+key.ID+"/reveal", map[string]any{}, &reveal); err != nil {
		return PoolKey{}, err
	}
	if reveal.ID != key.ID || reveal.Key == "" {
		return PoolKey{}, &Error{Code: "key_reveal_failed", HTTPStatus: 502}
	}
	return PoolKey{PoolID: pool.ID, KeyID: key.ID, Key: reveal.Key}, nil
}
