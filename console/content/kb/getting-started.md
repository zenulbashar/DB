---
title: Getting started
category: Getting started
order: 1
summary: From an empty platform to a connectable Postgres database in five minutes.
---

Zale DB is serverless PostgreSQL: you create a **project**, it comes with a
`main` **branch**, and every branch is a real Postgres you connect to with any
standard driver. Compute scales down to zero when idle and wakes on connect.

## 1. Get an API key

Everything is driven by an API key that starts with `zdb_`. If your platform was
just installed, the one-time **bootstrap** call creates the first organization
and owner key:

```bash
curl -X POST $NDB_API/v1/bootstrap \
  -H 'Content-Type: application/json' \
  -d '{"bootstrap_token":"<from your operator>","email":"you@example.com","org_name":"Acme"}'
```

The returned `api_key.token` is shown **exactly once** — store it in a secret
manager. On an existing platform, ask an org owner to create a key for you
(see *API keys & scopes*).

## 2. Sign in to the console

Open the console and paste your `zdb_` key on the **Connect** screen. The key is
kept in an httpOnly cookie; **Sign out** clears it.

## 3. Create a project

In the console: **New project** → pick a name, region, and Postgres version.
Or via the API:

```bash
curl -X POST $NDB_API/v1/projects \
  -H "Authorization: Bearer $NDB_KEY" -H 'Content-Type: application/json' \
  -d '{"org_id":"org_…","name":"my-app","region":"syd1","pg_version":17}'
```

Creation returns three things — the project, a seeded **owner role**
(`<name>_owner`) with its password, and a database named after the project. The
role password is shown **exactly once**; if you lose it, reset it (see
*Roles & databases*), don't look for a way to re-fetch it — there isn't one.

## 4. Connect

Your project's detail page shows a connection string for the default pooled
endpoint:

```
postgresql://my-app_owner:<password>@ep-….syd1.db.zaleit.com.au/my-app?sslmode=require
```

Use it with `psql`, Prisma, Drizzle, `pg` — anything that speaks Postgres. If
the branch was suspended, the first connection wakes it automatically (see
*Scale to zero*).

## Where to go next

- **Branches & data forks** — instant copies of your database for previews and testing.
- **Which endpoint do I use?** — pooled vs direct vs read-only.
- **Imports** — move an existing database (Neon, Supabase, RDS, …) into Zale DB.
