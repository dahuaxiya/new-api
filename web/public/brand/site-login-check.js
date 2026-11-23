// This frame runs on the consumer origin. Only its configured issuer may consume the tab's login intent.
window.addEventListener('message', function checkLoginIntent(event) {
  if (
    event.source !== parent ||
    parent === window ||
    event.data?.type !== 'luobo.login.check'
  )
    return
  let matched = false
  try {
    const stored = JSON.parse(
      sessionStorage.getItem('luobo.login.intent.v1') || 'null'
    )
    if (!stored || event.origin !== stored.issuer) return
    matched =
      stored.until > Date.now() &&
      stored.state === event.data.state &&
      stored.challenge === event.data.challenge
    if (matched) sessionStorage.removeItem('luobo.login.intent.v1')
  } catch {
    /* Storage restrictions must not grant authorization. */
  }
  event.source.postMessage(
    { type: 'luobo.login.checked', state: event.data.state, matched },
    event.origin
  )
})
