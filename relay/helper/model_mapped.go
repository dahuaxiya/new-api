package helper

import (
	"errors"
	"fmt"

	rootcommon "github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	hostreasoning "github.com/QuantumNous/new-api/setting/reasoning"
	"github.com/gin-gonic/gin"
)

// ModelMappedHelper applies the channel mapping to the relay metadata and outbound request before dispatch.
func ModelMappedHelper(c *gin.Context, info *relaycommon.RelayInfo, request dto.Request) error {
	if info.ChannelMeta == nil {
		info.ChannelMeta = &relaycommon.ChannelMeta{}
	}

	// map model name
	modelMapping := c.GetString("model_mapping")
	if modelMapping != "" && modelMapping != "{}" {
		modelMap := make(map[string]string)
		err := rootcommon.Unmarshal([]byte(modelMapping), &modelMap)
		if err != nil {
			return fmt.Errorf("unmarshal_model_mapping_failed")
		}

		target, identity := hostreasoning.ResolveChannelModel(modelMap, info.OriginModelName)
		if target == "" && info.OriginModelName != "" {
			return errors.New("model_mapping_contains_cycle")
		}
		// An explicit identity mapping preserves the caller's preconfigured upstream name and request.
		if identity {
			info.IsModelMapped = false
			return nil
		}
		if target != info.OriginModelName {
			info.IsModelMapped = true
		}
		if info.IsModelMapped {
			info.UpstreamModelName = target
		}
	}

	if request != nil {
		request.SetModelName(info.UpstreamModelName)
	}
	return nil
}
