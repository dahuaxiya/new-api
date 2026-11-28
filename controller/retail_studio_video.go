package controller

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/gin-gonic/gin"
)

const videoUnavailable = "The video is complete, but its file is unavailable. Open this record later to retry retrieval."
const videoUncertain = "Video submission could not be confirmed. Check usage before trying again."
const videoPending = "The video is still processing. Open this record later to continue checking."
const videoReview = "The video could not be delivered. Funds remain reserved while the charge is checked."

var studioVideoTaskID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var studioVideoClient = &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}

// studioVideoState 不进入历史 API。任务号、冻结报价和费用凭据必须先落盘，恢复时只能查询已受理任务。
type studioVideoState struct {
	Phase      string            `json:"phase"`
	TaskID     string            `json:"task_id,omitempty"`
	ChannelID  int               `json:"channel_id"`
	TokenID    int               `json:"token_id"`
	Group      string            `json:"group"`
	Quote      retailprice.Quote `json:"quote"`
	RetryAfter int64             `json:"retry_after"`
	Logged     bool              `json:"logged,omitempty"`
	Duration   int               `json:"duration,omitempty"`
}

// studioVideoAccountingPending 保护未完成核账的持久化凭据，读取失败时也保留记录供排查。
func studioVideoAccountingPending(key string) bool {
	raw, err := os.ReadFile(key + ".video-state")
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	var state studioVideoState
	if err != nil || common.Unmarshal(raw, &state) != nil {
		return true
	}
	return state.Phase != "finished" && state.Phase != "failed" && !(state.Phase == "settled" && state.Logged)
}

// storeStudioVideoState 原子替换私有状态，调用方持有账号锁，不能把凭据或上游 URL 写到公开记录。
func storeStudioVideoState(key string, state *studioVideoState) error {
	raw, err := common.Marshal(state)
	if err != nil {
		return err
	}
	if err = storeStudioImage(key+".video-state", raw); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(key))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// resumeStudioVideo 只恢复已有任务号的查询，单账号锁和运行槽防止多个页面并发拉取或重复结算。
func resumeStudioVideo(owner int, key string, row *studioHistoryRecord) bool {
	lock := &historyLocks[owner%len(historyLocks)]
	lock.Lock()
	defer lock.Unlock()
	if _, exists := studioRuns.Load(key); exists {
		return true
	}
	raw, err := os.ReadFile(key + ".video-state")
	var state studioVideoState
	if err != nil || common.Unmarshal(raw, &state) != nil || !studioVideoTaskID.MatchString(state.TaskID) || state.Phase == "failed" || state.Phase == "review" {
		return false
	}
	if state.Phase == "finished" {
		// 文件和完成凭据先于公开快照写盘。进程在两次写入之间退出时，
		// 直接从本地恢复展示，避免重新联网、扣费或把完成任务显示为中断。
		last := &row.Entries[len(row.Entries)-1]
		if !last.Complete || last.Video == "" {
			info, err := os.Stat(key + ".video")
			if err == nil && info.Size() > 0 {
				last.Video, last.Error, last.Complete = studioMediaPrefix+row.ID+"/video", "", true
				row.Running = false
				if storeStudioRecord(filepath.Dir(key), row) != nil {
					common.SysError("Unable to recover completed studio video")
				}
			}
		}
		return false
	}
	if state.RetryAfter > time.Now().Unix() {
		return false
	}
	select {
	case studioSlots <- struct{}{}:
	default:
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	worker := &studioWorker{cancel: cancel, video: true}
	studioRuns.Store(key, worker)
	go worker.executeVideo(ctx, owner, filepath.Dir(key), key, lock, row, nil)
	return true
}

// studioVideoSupply 每次联网前重新获取库存密钥。此适配器只支持已核实的星行源，禁止任意 URL 接收凭据。
func studioVideoSupply(channelID int) (string, error) {
	channel, err := model.GetChannelById(channelID, true)
	if err != nil || channel.Status != common.ChannelStatusEnabled {
		return "", retailprice.ErrAccess
	}
	base, err := url.Parse(channel.GetBaseURL())
	if err != nil || base.Scheme != "https" || base.Host != "aixingxing.pro" || base.User != nil || (base.Path != "" && base.Path != "/" && base.Path != "/v1") || base.RawQuery != "" || base.Fragment != "" {
		return "", retailprice.ErrAccess
	}
	return channel.SupplySecret(model.DB)
}

// studioVideoJSON 限制单次响应体并拒绝重定向，错误信息不携带请求头、密钥或供应商返回正文。
func studioVideoJSON(ctx context.Context, channelID int, method, path string, payload any, output any) (int, error) {
	secret, err := studioVideoSupply(channelID)
	if err != nil {
		return 0, err
	}
	var body []byte
	if payload != nil {
		body, err = common.Marshal(payload)
		if err != nil {
			return 0, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://aixingxing.pro"+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	response, err := studioVideoClient.Do(req)
	if err != nil {
		return 0, errors.New(videoUncertain)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return response.StatusCode, errors.New(videoUncertain)
	}
	if response.StatusCode != http.StatusOK || common.Unmarshal(raw, output) != nil {
		return response.StatusCode, errors.New(videoUncertain)
	}
	return response.StatusCode, nil
}

// executeVideo 将一次生成与一笔预留绑定。提交状态写盘后永不自动重发，网络结果不明确时保留预留供核对。
func (w *studioWorker) executeVideo(ctx context.Context, owner int, dir, key string, lock *sync.Mutex, row *studioHistoryRecord, video *retailprice.VideoRequest) {
	var state studioVideoState
	var runErr error
	last := &row.Entries[len(row.Entries)-1]
	chargeID := "studio-video/" + row.ID
	save := func() error { lock.Lock(); defer lock.Unlock(); return storeStudioVideoState(key, &state) }
	defer func() {
		if recover() != nil {
			runErr = errors.New(videoUncertain)
		}
		w.cancel()
		if runErr != nil {
			last.Error = runErr.Error()
			last.Complete = false
		} else {
			last.Error = ""
			last.Complete = true
		}
		row.Running = false
		row.UpdatedAt = time.Now().UnixMilli()
		lock.Lock()
		if storeStudioRecord(dir, row) != nil {
			common.SysError("Unable to finalize studio video")
		}
		studioRuns.Delete(key)
		lock.Unlock()
		<-studioSlots
	}()
	if video != nil {
		token, err := model.RetailStudioToken(owner, true, row.Product)
		if err != nil {
			runErr = common.NewMessage("Unable to load retail pricing")
			return
		}
		state.TokenID = token.Id
		group, err := model.RetailTokenGroup(owner, token.Id)
		if err != nil {
			runErr = common.NewMessage("Unable to load retail pricing")
			return
		}
		state.Group = group
		eligible, err := model.RetailSupplyIDs(group)
		if err != nil {
			runErr = common.NewMessage("Unable to load retail pricing")
			return
		}
		quote, err := model.RetailVideoQuote(owner, token.Id, last.Model, *video, eligible, row.Product)
		if err != nil || quote == nil || len(quote.Channels) == 0 {
			runErr = common.NewMessage("Unable to load retail pricing")
			return
		}
		state.Quote, state.ChannelID, state.Phase = *quote, quote.Channels[0], "prepared"
		if _, err = studioVideoSupply(state.ChannelID); err != nil {
			runErr = common.NewMessage("Unable to load retail pricing")
			return
		}
		if err = save(); err != nil {
			runErr = errors.New(videoUncertain)
			return
		}
		if _, err = model.ReservePricedRetailCharge(chargeID, owner, token.Id, quote); err != nil {
			// 预留没有成功，也没有提交上游，移除准备状态使失败记录可以正常删除。
			lock.Lock()
			_ = removeStudioMedia(key + ".video-state")
			lock.Unlock()
			runErr = common.NewMessage("Unable to reserve retail funds")
			return
		}
		// 钱包转换会把预留金额向上取整，恢复凭据必须保存实际预留后的冻结报价。
		state.Quote = *quote
		// 必须先提交状态再发 HTTP。崩溃发生在这两步之间时宁可人工核对，也不能再发一次付费请求。
		state.Phase = "submitting"
		if err = save(); err != nil {
			// 尚未执行 HTTP，可以确定没有付费任务，安全退回本次预留。
			if model.ChangeRetailCharge(chargeID, owner, state.TokenID, 0, "refunded") == nil {
				state.Phase = "failed"
				_ = save()
			}
			runErr = errors.New(videoUncertain)
			return
		}
		var accepted struct {
			RequestID string `json:"request_id"`
		}
		status, err := studioVideoJSON(ctx, state.ChannelID, http.MethodPost, "/xai/v1/videos/generations", gin.H{"model": state.Quote.VideoModel, "prompt": last.Prompt, "duration": video.Duration, "resolution": video.Resolution, "aspect_ratio": video.AspectRatio}, &accepted)
		if err != nil || !studioVideoTaskID.MatchString(accepted.RequestID) {
			// 仅明确拒绝的请求退预留，超时、5xx、无任务号的成功响应都可能已被上游受理。
			if status == 400 || status == 401 || status == 403 || status == 404 || status == 422 {
				if model.ChangeRetailCharge(chargeID, owner, state.TokenID, 0, "refunded") == nil {
					state.Phase = "failed"
					_ = save()
				}
			}
			runErr = errors.New(videoUncertain)
			return
		}
		state.TaskID, state.Phase = accepted.RequestID, "accepted"
		if save() != nil {
			runErr = errors.New(videoUncertain)
			return
		}
	} else {
		raw, err := os.ReadFile(key + ".video-state")
		if err != nil || common.Unmarshal(raw, &state) != nil || !studioVideoTaskID.MatchString(state.TaskID) {
			runErr = errors.New(videoUncertain)
			return
		}
	}
	last.Error = ""
	state.RetryAfter = time.Now().Add(time.Minute).Unix()
	if save() != nil {
		runErr = errors.New(videoUncertain)
		return
	}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		var result struct {
			Status string `json:"status"`
			Video  struct {
				Duration int    `json:"duration"`
				URL      string `json:"url"`
			} `json:"video"`
		}
		// 已完成任务可能从上游过期。使用落盘的完成事实恢复结算与下载，不能重新生成或退款。
		if state.Phase == "settled" || state.Phase == "completed" {
			result.Status, result.Video.Duration = "done", state.Duration
		} else {
			_, err := studioVideoJSON(ctx, state.ChannelID, http.MethodGet, "/xai/v1/videos/"+state.TaskID, nil, &result)
			if err != nil {
				runErr = errors.New(videoPending)
				return
			}
		}
		switch result.Status {
		case "done":
			state.Duration = result.Video.Duration
			state.Phase = "completed"
			if save() != nil {
				runErr = errors.New(videoUncertain)
				return
			}
			amount, err := state.Quote.ChargeVideo(result.Video.Duration)
			if err != nil {
				runErr = errors.New(videoUncertain)
				return
			}
			quota, _, err := model.RetailCNYQuota(amount, state.Quote.WalletCNYPerUnit)
			if err != nil || model.ChangeRetailCharge(chargeID, owner, state.TokenID, quota, "settled") != nil {
				runErr = errors.New(videoUncertain)
				return
			}
			state.Phase = "settled"
			// 独立日志库与钱包无法共享事务。先按稳定请求号写日志，再保存标记，
			// 恢复时查重，可重试日志库故障且不会因两次写入之间崩溃丢失消费日志。
			if !state.Logged {
				other := model.NewLogOther()
				other.SetPublic("video_duration", state.Duration)
				other.SetPublic("video_resolution", state.Quote.Video.Resolution)
				other.SetPublic("retail_charge_cny", amount.String())
				other.SetAdmin("studio_run_id", row.ID)
				other.SetAdmin("upstream_task_id", state.TaskID)
				if err := model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{UserId: owner, LogType: model.LogTypeConsume, ChannelId: state.ChannelID, ModelName: last.Model, Quota: quota, TokenId: state.TokenID, Group: state.Group, Other: other, RequestId: chargeID, UpstreamRequestId: state.TaskID}); err != nil {
					runErr = errors.New(videoUnavailable)
					return
				}
				state.Logged = true
				if save() != nil {
					runErr = errors.New(videoUncertain)
					return
				}
			}
			if err = storeStudioVideoFile(ctx, key, state.ChannelID, state.TaskID); err != nil {
				_ = save()
				runErr = errors.New(videoUnavailable)
				return
			}
			last.Video = studioMediaPrefix + row.ID + "/video"
			state.Phase = "finished"
			if save() != nil {
				runErr = errors.New(videoUnavailable)
			}
			return
		case "failed", "expired":
			// 上游审核失败仍可能收费，过期也不能证明未生成。保留冻结凭据，
			// 停止自动重试，等待确认真实费用后再结算或退款。
			state.Phase = "review"
			_ = save()
			runErr = errors.New(videoReview)
			return
		}
		select {
		case <-ctx.Done():
			runErr = errors.New(videoPending)
			return
		case <-ticker.C:
		}
	}
}

// storeStudioVideoFile 从已受理任务的制品列表取得访问地址，只把验证后的 MP4 原子写入私有目录。
// 任务状态中的旧视频地址不能用于下载。访问凭证仅在本次请求内使用，不写入历史、恢复状态或错误。
func storeStudioVideoFile(ctx context.Context, key string, channelID int, taskID string) error {
	if !studioVideoTaskID.MatchString(taskID) {
		return errors.New(videoUnavailable)
	}
	var listing struct {
		TaskID    string                 `json:"task_id"`
		Artifacts []taskArtifactResponse `json:"artifacts"`
	}
	_, err := studioVideoJSON(ctx, channelID, http.MethodGet, "/v1/tasks/"+taskID+"/artifacts", nil, &listing)
	if err != nil || listing.TaskID != taskID {
		return errors.New(videoUnavailable)
	}
	var artifact taskArtifactResponse
	for _, item := range listing.Artifacts {
		if item.Type == "video" && item.MimeType == "video/mp4" && taskArtifactKeyPattern.MatchString(item.Key) {
			artifact = item
			break
		}
	}
	target, err := url.Parse(artifact.ContentURL)
	if err != nil || target.User != nil || target.Fragment != "" {
		return errors.New(videoUnavailable)
	}
	if target.IsAbs() && (target.Scheme != "https" || target.Host != "aixingxing.pro") {
		return errors.New(videoUnavailable)
	}
	if target.Host != "" && target.Host != "aixingxing.pro" {
		return errors.New(videoUnavailable)
	}
	// 只允许当前任务的制品内容路由，防止供应商异常响应把库存密钥或访问凭证带到其他位置。
	expectedPath := "/v1/tasks/" + taskID + "/artifacts/" + artifact.Key + "/content"
	if artifact.Key == "" || target.Path != expectedPath || target.RawPath != "" {
		return errors.New(videoUnavailable)
	}
	secret, err := studioVideoSupply(channelID)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://aixingxing.pro"+target.RequestURI(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	response, err := studioVideoClient.Do(req)
	if err != nil {
		return errors.New(videoUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New(videoUnavailable)
	}
	file, err := os.CreateTemp(filepath.Dir(key), "video-*.part")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	size, err := io.Copy(file, io.LimitReader(response.Body, (76<<20)+1))
	if err != nil || size == 0 || size > 76<<20 {
		return errors.New(videoUnavailable)
	}
	var signature [512]byte
	count, err := file.ReadAt(signature[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if http.DetectContentType(signature[:count]) != "video/mp4" {
		return errors.New(videoUnavailable)
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), key+".video")
}
