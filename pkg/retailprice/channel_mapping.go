package retailprice

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/reasoning"
)

// ChannelMapping distinguishes a malformed mapping from an absent (identity) mapping.
type ChannelMapping struct {
	values  map[string]string
	invalid bool
}

// ParseChannelMapping decodes once per channel; unreadable operator settings cannot authorize an offer.
func ParseChannelMapping(raw string) ChannelMapping {
	var mapping ChannelMapping
	if raw != "" {
		mapping.invalid = common.UnmarshalJsonStr(raw, &mapping.values) != nil
	}
	return mapping
}

// Target returns the final dispatched ID, or an empty string if no route can be verified.
func (m ChannelMapping) Target(name string) string {
	if m.invalid {
		return ""
	}
	target, _ := reasoning.ResolveChannelModel(m.values, name)
	return target
}
