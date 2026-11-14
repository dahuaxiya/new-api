package service

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestSiteCookiesSurviveOtherSiteLoginAndLogout 模拟同一 IP 不同端口，保护托管站已有登录不被中转站覆盖或清除。
func TestSiteCookiesSurviveOtherSiteLoginAndLogout(t *testing.T) {
	previousSecure := common.SessionCookieSecure
	common.SessionCookieSecure = true
	t.Cleanup(func() { common.SessionCookieSecure = previousSecure })
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	hostingURL, _ := url.Parse("https://192.0.2.1:10443/api/user/auth/refresh")
	retailURL, _ := url.Parse("https://192.0.2.1:11443/api/user/auth/refresh")

	for _, mode := range []string{"hosting", "retail"} {
		t.Setenv("SITE_MODE", mode)
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		WriteRefreshCookie(ctx, mode+"-test-token")
		cookies := recorder.Result().Cookies()
		require.Len(t, cookies, 2)
		for _, cookie := range cookies {
			require.True(t, cookie.Secure)
			require.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
		}
		require.True(t, cookies[0].HttpOnly)
		require.Equal(t, "/api/user/auth", cookies[0].Path)
		jar.SetCookies(retailURL, cookies)
	}

	values := map[string]string{}
	for _, cookie := range jar.Cookies(hostingURL) {
		values[cookie.Name] = cookie.Value
	}
	require.Equal(t, "hosting-test-token", values["new_api_refresh"])
	require.Equal(t, "retail-test-token", values["new_api_retail_refresh"])

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ClearRefreshCookie(ctx)
	jar.SetCookies(retailURL, recorder.Result().Cookies())
	values = map[string]string{}
	for _, cookie := range jar.Cookies(hostingURL) {
		values[cookie.Name] = cookie.Value
	}
	require.Equal(t, "hosting-test-token", values["new_api_refresh"])
	require.Equal(t, "1", values["new_api_has_session"])
	require.NotContains(t, values, "new_api_retail_refresh")
	require.NotContains(t, values, "new_api_retail_has_session")
}
