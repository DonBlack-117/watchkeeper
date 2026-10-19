# Problem types

Errors are returned as [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) problem details with `Content-Type: application/problem+json`.

## bad-request

`400`. The body is not valid JSON or has fields the API does not know.

## unauthorized

`401`. `WK_API_TOKEN` is set and the request has no valid `Authorization: Bearer <token>` header. `/api/v1/status` and `/api/v1/health` never require it.

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

`409`. The check limit (`WK_MAX_CHECKS`, 200 by default) has been reached.

## rate-limited

`429`. One client made more than 60 requests per minute to `/api/v1/status`. Retry after the time in `Retry-After`.

## internal

`500`. Something failed on the server. Details are written to the log, never to the response.
