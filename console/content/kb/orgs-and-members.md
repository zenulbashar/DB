---
title: Organizations & members
category: Access & security
order: 2
summary: The tenancy model, member roles, and how to manage your team.
---

An **organization** is the tenancy and billing boundary. Projects belong to an
org; API keys are issued by an org; members join an org with a role.

## Member roles

| Role | Can |
|---|---|
| `owner` | everything, including managing members and keys; cannot be removed if they're the last owner |
| `admin` | manage projects/branches/keys; not membership |
| `member` | day-to-day project and branch work |
| `viewer` | read-only |

## Managing members

```bash
GET    /orgs/{org}/members
POST   /orgs/{org}/members            {"email":"dev@acme.com","role":"member"}
PATCH  /orgs/{org}/members/{user}     {"role":"admin"}
DELETE /orgs/{org}/members/{user}
```

Safety rails:

- The **last owner** cannot be demoted or removed (`409`) — promote someone
  else to owner first.
- Adding a member by email creates the user record if it doesn't exist yet;
  email sign-in for the console is on the roadmap (today, members act through
  API keys).

## Practical setup for a small team

1. Bootstrap gives you the first org and an owner key.
2. Add teammates as `member`; promote one to `owner` as your bus-factor
   backup.
3. Issue each person their own API key (see *API keys & scopes*) instead of
   sharing one — the audit log then tells you *who* did everything.

## Renaming

`PATCH /orgs/{org} {"name":"New Name"}` — the slug and ID stay stable, so
nothing breaks.
