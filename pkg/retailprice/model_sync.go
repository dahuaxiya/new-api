package retailprice

import (
	"slices"
	"strconv"
)

// ModelSyncState records only model IDs. A second complete observation confirms a removal.
type ModelSyncState struct {
	Missing []string `json:"missing,omitempty"`
	Paused  []string `json:"paused,omitempty"`
	Pending []string `json:"pending,omitempty"`
	Failed  bool     `json:"failed,omitempty"`
}

// SyncID isolates observations for local groups sharing one credential.
func (b GroupBinding) SyncID() string { return strconv.Itoa(b.ChannelID) + ":" + b.LocalGroup }

// ApplyModelList updates only the generated group product. Existing tariffs, permissions and aliases
// belong to the operator. Empty or failed reads never count as evidence that a model disappeared.
// The returned public names must be added to channel abilities in the same transaction.
func (b *Book) ApplyModelList(binding GroupBinding, upstream []string, mapping map[string]string, now int64) []string {
	if !binding.FollowModels {
		return nil
	}
	if b.ModelSync == nil {
		b.ModelSync = map[string]ModelSyncState{}
	}
	key := binding.SyncID()
	old := b.ModelSync[key]
	state := ModelSyncState{}
	if len(upstream) == 0 {
		old.Failed = true
		b.ModelSync[key] = old
		return nil
	}
	present := map[string]bool{}
	for _, id := range upstream {
		present[id] = true
	}
	prices := map[string]Model{}
	existing := map[string]bool{}
	for _, m := range OfficialModels() {
		prices[m.ID] = m
	}
	for _, m := range b.Models {
		prices[m.ID] = m
		existing[m.ID] = true
	}
	id := officialProductID(binding.LocalGroup)
	p := Product{ID: id, Name: binding.LocalGroup, BindingGroup: binding.LocalGroup, Public: true}
	pi := slices.IndexFunc(b.Products, func(v Product) bool { return v.ID == id })
	if pi >= 0 {
		p = b.Products[pi]
	}
	// Never upgrade legacy private access or a renamed/custom product by inference.
	if p.BindingGroup != binding.LocalGroup {
		old.Failed = true
		b.ModelSync[key] = old
		return nil
	}
	ri := slices.IndexFunc(p.Routes, func(v Route) bool { return v.ChannelID == binding.ChannelID })
	route := Route{ChannelID: binding.ChannelID, Models: map[string]string{}}
	if ri >= 0 {
		route = p.Routes[ri]
	}
	recognized := map[string]bool{}
	abilities := []string{}
	for public, target := range route.Models {
		if price, ok := prices[public]; ok && price.VerifiedAt <= now && price.ValidUntil > now && present[target] {
			recognized[target] = true
		}
	}
	for _, name := range upstream {
		if recognized[name] {
			continue
		}
		m, ok := prices[name]
		target := mapping[name]
		// Only new routes need an explicit supported modality. Historical routes keep their contract.
		if !ok || (m.Kind != "text" && m.Kind != "image") || m.OfficialModel != name || m.VerifiedAt > now || m.ValidUntil <= now || (target != "" && target != name) || (route.Models[name] != "" && route.Models[name] != name) {
			state.Pending = append(state.Pending, name)
			continue
		}
		if !existing[name] {
			b.Models = append(b.Models, m)
			existing[name] = true
		}
		route.Models[name] = name
	}
	for public, target := range route.Models {
		if present[target] {
			abilities = append(abilities, public)
			continue
		}
		state.Missing = append(state.Missing, target)
		if slices.Contains(old.Missing, target) || slices.Contains(old.Paused, target) {
			state.Paused = append(state.Paused, target)
		}
	}
	// Keep operator mappings even for paused models so a confirmed return is reversible.
	for _, list := range []*[]string{&state.Missing, &state.Paused, &state.Pending} {
		slices.Sort(*list)
		*list = slices.Compact(*list)
	}
	b.ModelSync[key] = state
	if len(route.Models) > 0 {
		if ri < 0 {
			p.Routes = append(p.Routes, route)
		}
		if pi < 0 {
			b.Products = append(b.Products, p)
		} else {
			b.Products[pi] = p
		}
	}
	slices.Sort(abilities)
	return abilities
}
