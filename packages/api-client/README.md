# @nimbusdb/api-client

TypeScript types for the Zale DB control-plane API, **generated** from
[`/api/openapi.yaml`](../../api/openapi.yaml).

Spec-first rule (ADR-012): change the OpenAPI document first, then run

```sh
npm run generate     # or `make generate` from the repo root
```

`src/schema.d.ts` is generated output — never edit it by hand; CI regenerates
and fails on drift. `src/index.ts` holds the thin hand-written fetch wrapper.
