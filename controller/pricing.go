package controller

import (
	"errors"
	"net/http"
	"os"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// filterPricingByUsableGroups 裁剪模型和模型内的分组；托管账号还要匹配绑定渠道的能力。
// 返回的新分组切片不能修改 model.GetPricing 返回的共享缓存。
func filterPricingByUsableGroups(pricing []model.Pricing, usableGroup map[string]string, hostingModels map[string]map[string]bool) []model.Pricing {
	if len(pricing) == 0 {
		return pricing
	}
	if len(usableGroup) == 0 {
		return []model.Pricing{}
	}

	filtered := make([]model.Pricing, 0, len(pricing))
	for _, item := range pricing {
		groups := make([]string, 0, len(item.EnableGroup))
		for _, group := range item.EnableGroup {
			if hostingModels != nil {
				if hostingModels[group][item.ModelName] {
					groups = append(groups, group)
				}
			} else if _, ok := usableGroup[group]; ok || group == "all" {
				groups = append(groups, group)
			}
		}
		if len(groups) > 0 {
			item.EnableGroup = groups
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// pricingHostingModels 只使用已鉴权用户的有效名额及其绑定渠道。
// 存在名额时即使全部失效也不回退旧分组；数据库异常交给调用方拒绝本次查询。
func pricingHostingModels(userID int) (map[string]map[string]bool, bool, error) {
	if userID <= 0 || os.Getenv("GPT_HOSTING_ENABLED") != "true" {
		return nil, false, nil
	}
	seats, err := model.ListHostingSeats(userID)
	if err != nil || len(seats) == 0 {
		return nil, false, err
	}
	groups := make(map[string]map[string]bool)
	for _, seat := range seats {
		group, channelID, err := model.GetHostingRoute(userID, seat.SeatID)
		if errors.Is(err, model.ErrHostingUnavailable) || errors.Is(err, gorm.ErrRecordNotFound) {
			// 过期、暂停或尚未绑定的名额不能借旧组展示其他账号的模型。
			continue
		}
		if err != nil {
			return nil, true, err
		}
		if group == "" || channelID == 0 {
			continue
		}
		models, err := model.GetChannelGroupEnabledModels(channelID, group)
		if err != nil {
			return nil, true, err
		}
		if groups[group] == nil {
			groups[group] = make(map[string]bool)
		}
		for _, name := range models {
			groups[group][name] = true
		}
	}
	return groups, true, nil
}

// GetPricing 按登录身份返回模型价格；管理员可查看全站，普通用户只看到自身可用的模型和分组。
// 本接口只决定展示范围，不改变令牌权限、托管绑定或实际请求路由。
func GetPricing(c *gin.Context) {
	pricing := model.GetPricing()
	userID := c.GetInt("id")
	var group string
	if userID > 0 {
		user, err := model.GetUserCache(userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Unable to load model pricing"})
			return
		}
		group = user.Group
	}

	usableGroup := service.GetUserUsableGroups(group)
	autoGroups := service.GetUserAutoGroup(group)
	if userID > 0 && c.GetInt("role") >= common.RoleAdminUser {
		// 管理员目录包含渠道分组，即使该分组未加入公开可选分组或倍率配置。
		for g := range ratio_setting.GetGroupRatioCopy() {
			if _, ok := usableGroup[g]; !ok {
				usableGroup[g] = "Group"
			}
		}
		for _, item := range pricing {
			for _, g := range item.EnableGroup {
				if _, ok := usableGroup[g]; !ok {
					usableGroup[g] = "Group"
				}
			}
		}
	} else {
		hostingModels, hasSeats, err := pricingHostingModels(userID)
		if err != nil {
			// 查不到权益时不能把用户放回旧分组，以免返回不属于该账号的渠道信息。
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Unable to load model pricing"})
			return
		}
		if hasSeats {
			usableGroup = make(map[string]string, len(hostingModels))
			for g := range hostingModels {
				usableGroup[g] = "User group"
			}
			autoGroups = []string{}
		}
		pricing = filterPricingByUsableGroups(pricing, usableGroup, hostingModels)
	}

	configuredRatios := ratio_setting.GetGroupRatioCopy()
	groupRatio := make(map[string]float64, len(usableGroup))
	for g := range usableGroup {
		if ratio, ok := ratio_setting.GetGroupGroupRatio(group, g); ok {
			groupRatio[g] = ratio
		} else if ratio, ok := configuredRatios[g]; ok {
			groupRatio[g] = ratio
		} else {
			// 与实际路由的默认倍率一致，避免未显式配置的分组在页面上丢失价格。
			groupRatio[g] = 1
		}
	}

	c.JSON(200, gin.H{
		"success":            true,
		"data":               pricing,
		"vendors":            model.GetVendors(),
		"group_ratio":        groupRatio,
		"usable_group":       usableGroup,
		"supported_endpoint": model.GetSupportedEndpointMap(),
		"auto_groups":        autoGroups,
		"pricing_version":    "a42d372ccf0b5dd13ecf71203521f9d2",
	})
}

func ResetModelRatio(c *gin.Context) {
	defaultStr := ratio_setting.DefaultModelRatio2JSONString()
	err := model.UpdateOption("ModelRatio", defaultStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	err = ratio_setting.UpdateModelRatioByJSONString(defaultStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccessT(c, "Model ratios reset", nil)
}
