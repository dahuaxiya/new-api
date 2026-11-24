package controller

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/stretchr/testify/require"
)

// TestParseRetailAvailability 验证时间片顺序、总量校验、空窗口和过期数据的边界。
func TestParseRetailAvailability(t *testing.T) {
	now := time.Unix(1795521066, 0)
	buckets := `[[3,0],[0,2],[1,1]` + strings.Repeat(`,[0,0]`, 15) + `]`
	payload := `{"updated":%d,"groups":{"source":{"models":{"remote":true},"windows":{"90m":{"remote":%s},"24h":{}}}}}`
	body := []byte(fmt.Sprintf(payload, now.Unix(), `[4,3,null,null,null,null,null,null,`+buckets+`]`))
	snapshot, err := parseRetailAvailability(body, now)
	require.NoError(t, err)
	series := snapshot.groups["source"]["remote"]
	require.Equal(t, [retailAvailabilitySlices]retailSliceStatus{retailSliceSuccess, retailSliceFailure, retailSliceSuccess | retailSliceFailure}, series.Short)
	require.Zero(t, series.Day)
	_, err = parseRetailAvailability(body, now.Add(retailAvailabilityMaxAge+time.Second))
	require.ErrorIs(t, err, errRetailAvailability)

	malformedBuckets := []string{`[[4,3]]`, strings.Replace(buckets, "[3,0]", "[null,0]", 1), strings.Replace(buckets, "[3,0]", "[-3,0]", 1), strings.Replace(buckets, "[3,0]", "[2,0]", 1)}
	for _, invalid := range malformedBuckets {
		body := []byte(fmt.Sprintf(payload, now.Unix(), `[4,3,null,null,null,null,null,null,`+invalid+`]`))
		_, err := parseRetailAvailability(body, now)
		require.ErrorIs(t, err, errRetailAvailability, invalid)
	}
}

// TestProjectRetailAvailability 验证公开内容只含本站分组和状态，且暂停线路和非来源站线路不参与判定。
func TestProjectRetailAvailability(t *testing.T) {
	book := retailprice.Book{
		CredentialGroups: map[int]string{1: "upstream-a", 2: "upstream-b", 3: "upstream-b", 4: "upstream-b", 5: "upstream-a"},
		Products: []retailprice.Product{
			{Public: true, BindingGroup: "gpt-normal", Routes: []retailprice.Route{
				{ChannelID: 1, Models: map[string]string{"alias-1": "remote-a"}},
				{ChannelID: 5, Models: map[string]string{"alias-1": "remote-a"}},
			}},
			{Public: true, BindingGroup: "gpt-normal", Routes: []retailprice.Route{{ChannelID: 2, Models: map[string]string{"alias-2": "remote-b"}}}},
			{Public: true, BindingGroup: "gemini-ultra", Routes: []retailprice.Route{{ChannelID: 3, Models: map[string]string{"alias-2": "remote-b"}}, {ChannelID: 4, Models: map[string]string{"alias-2": "remote-b"}}}},
			{BindingGroup: "private-group", Routes: []retailprice.Route{{ChannelID: 1, Models: map[string]string{"alias-1": "remote-a"}}}},
		},
	}
	channels := []model.RetailBindingChannel{
		{ID: 1, HasKey: true, Status: common.ChannelStatusEnabled},
		{ID: 2, HasKey: true, Status: common.ChannelStatusEnabled},
		{ID: 3, HasKey: true, Status: common.ChannelStatusManuallyDisabled},
		{ID: 5, HasKey: true, Status: common.ChannelStatusEnabled},
	}
	snapshot := &retailAvailabilitySnapshot{groups: map[string]map[string]retailAvailabilitySeries{
		"upstream-a": {"remote-a": {Short: [retailAvailabilitySlices]retailSliceStatus{retailSliceSuccess, retailSliceSuccess}}},
		"upstream-b": {"remote-b": {Short: [retailAvailabilitySlices]retailSliceStatus{retailSliceFailure}, Day: [retailAvailabilitySlices]retailSliceStatus{retailSliceFailure}}},
	}}
	rows := projectRetailAvailability(book, channels, snapshot)
	require.Len(t, rows, 2)
	require.Equal(t, "gemini-ultra", rows[0].Group)
	require.False(t, rows[0].Observed)
	for i := range retailAvailabilitySlices {
		require.Equal(t, "empty", rows[0].Short[i])
		require.Equal(t, "empty", rows[0].Day[i])
	}
	require.Equal(t, "gpt-normal", rows[1].Group)
	require.True(t, rows[1].Observed)
	require.Equal(t, []string{"mixed", "success", "empty"}, rows[1].Short[:3])
	require.Equal(t, "failure", rows[1].Day[0])
	encoded, err := json.Marshal(rows)
	require.NoError(t, err)
	for _, internal := range []string{"upstream-", "remote-", "private-group", "channel", "alias-"} {
		require.NotContains(t, string(encoded), internal)
	}
}
