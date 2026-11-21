package service

import (
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/stretchr/testify/require"
	"net/url"
	"testing"
)

// TestRetailDispatchRateOrigin 验证多来源实际转发时只放行所选线路自己的域名，未迁移的线路仍走原站点。
func TestRetailDispatchRateOrigin(t *testing.T) {
	book := retailprice.Book{GroupBindings: []retailprice.GroupBinding{{ChannelID: 1}, {ChannelID: 2}}, RateSiteByChannel: map[int]int{2: 12}, SiteFeeds: map[int]retailprice.SiteRateFeed{12: {URL: "https://other.example/rates"}}}
	a, _ := url.Parse("https://aixingxing.pro/v1/responses")
	b, _ := url.Parse("https://other.example/v1/chat/completions")
	require.True(t, retailDispatchOriginMatches(book, 1, a))
	require.False(t, retailDispatchOriginMatches(book, 1, b))
	require.True(t, retailDispatchOriginMatches(book, 2, b))
	require.False(t, retailDispatchOriginMatches(book, 2, a))
}
