/** Resolve the public API base without duplicating /v1 or exposing URL credentials.
 * Only administrator-configured HTTP(S) endpoints are used; invalid values fall
 * back to the current site. JSON string encoding below keeps TOML values literal.
 */
export function docsApiBase(address: unknown, origin: string): string {
  try {
    const url = new URL(
      typeof address === 'string' && address.trim() ? address.trim() : origin
    )
    if (
      !['http:', 'https:'].includes(url.protocol) ||
      url.username ||
      url.password
    ) {
      throw new Error('Invalid public API address')
    }
    url.search = ''
    url.hash = ''
    const path = url.pathname.replace(/\/+$/, '')
    url.pathname = path.endsWith('/v1') ? path : `${path}/v1`
    return url.toString().replace(/\/$/, '')
  } catch {
    return `${origin.replace(/\/+$/, '')}/v1`
  }
}

/** Return a copyable provider example containing a placeholder, never a live key.
 * Keep permissions and existing desktop/plugin settings outside this snippet.
 */
export function codexConfig(baseUrl: string): string {
  return `model = "gpt-6.1-sol"
model_provider = "my_api"
model_reasoning_effort = "medium"

[model_providers.my_api]
name = "My API"
base_url = ${JSON.stringify(baseUrl)}
wire_api = "responses"
experimental_bearer_token = "YOUR_API_KEY"
requires_openai_auth = false
supports_websockets = false`
}
