---
title: "Introduction"
description: "What rtd is and how it is put together."
weight: 10
---

Browse and search ReadTheDocs documentation projects

rtd is a single binary. It speaks to rtd over plain HTTPS,
shapes the responses into clean records, and gets out of your way. There is
nothing to sign up for and nothing to run alongside it.

## How it is built

- A **library package** (`rtd`) holds the HTTP client and the typed
  data models. It paces requests, sets an honest User-Agent, and retries the
  transient failures any public site throws under load.
- A **domain** (`rtd/domain.go`) declares each operation once on the
  [any-cli/kit](https://github.com/tamnd/any-cli) framework. That single
  declaration becomes a CLI command, an HTTP route, an MCP tool, and a
  resource-URI dereference. It is the one place you add to the tool.
- A thin **`cmd/rtd`** hands the assembled app to `kit.Run`, which
  builds the command tree and the serve and mcp surfaces.

## One operation, four surfaces

Because an operation is surface-neutral, the same `page` you run on the command
line is also a route and a tool:

```bash
rtd page <path>                  # the command
rtd serve --addr :7777           # GET /v1/page/<path>
rtd mcp                          # the page tool, over stdio
ant get rtd://page/<path>        # the URI dereference (via a host)
```

You write the fetch and the record shape; the surfaces come for free.

## Scope

rtd is a read-only client over data rtd already serves
publicly. It reads that data and shapes it for you. That narrow scope keeps it a
single small binary with no database, no daemon, and no setup.

Next: [install it](/getting-started/installation/), then take the
[quick start](/getting-started/quick-start/).
