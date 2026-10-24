# Problem types

Errors are returned as [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) problem details with `Content-Type: application/problem+json`.

## bad-request

`400`. The body is not valid JSON or has fields the API does not know.

## unauthorized

`401`. Authentication is on (`WK_API_TOKEN` or `WK_ADMIN_USER` is set) and the request has neither a valid `Authorization: Bearer <token>` header nor a dashboard session. A failed login also returns it, with the detail `wrong username or password`. `/api/v1/status`, `/api/v1/health` and the login endpoints never require it.

## forbidden

`403`. A request that changes data with a session cookie came without an `Origin` header that matches the host. Browsers send it on every `POST`, `PATCH` and `DELETE`, so this blocks cross-site request forgery. Scripts should use the bearer token instead of the cookie.

## not-found

`404`. The check does not exist. Ids that are not positive integers also return 404.

## validation

`422`. One or more fields are invalid. `errors` lists each field with a message:

```json
{
  "type": "https://github.com/DonBlack-117/watchkeeper/blob/main/docs/problems.md#validation",
  "title": "Validation failed",
  "status": 422,
  "detail": "one or more fields are invalid",
  "errors": [
    { "field": "intervalS", "message": "must be between 30 and 86400" }
  ]
}
```

## conflict

`409`. The check limit (`WK_MAX_CHECKS`, 200 by default) has been reached, an investigation of the incident is already running, or AI triage is not configured (`WK_ANTHROPIC_API_KEY`).

## rate-limited

`429`. One client made more than 60 requests per minute to `/api/v1/status`, more than 10 login attempts per minute, more than 5 manual investigations per minute, or the daily limit of investigations (`WK_TRIAGE_DAILY_LIMIT`) was reached. Retry after the time in `Retry-After`.

## unavailable

`503`. The queue of AI investigations is full. Retry after the time in `Retry-After`.

## bad-gateway

`502`. `POST /api/v1/alerts/test` could not deliver the message. The detail has Telegram's answer; `401 Unauthorized` means a wrong bot token and `400 Bad Request: chat not found` a wrong chat id or a bot that was never added to the chat.

## internal

`500`. Something failed on the server. Details are written to the log, never to the response.
