# VOD Manager

[Português](README.md) · **English**

> Made in Brazil 🇧🇷 — **the source code and its comments are written in Portuguese.**
> This README is translated; the codebase is not. Worth knowing before you dive in.

A video-on-demand library manager. It merges the catalog of several upstream sources into one
library, delivers the videos to your clients, and keeps a local copy of what gets watched — so
the second person to open a film costs the source nothing.

**It does not distribute content.** Sources are registered by whoever administers the
installation; the system only organizes, proxies, and stores what those sources already serve.

## The problem it solves

Anyone running a VOD service on top of third-party sources hits the same walls:

- **The same film shows up six times**, because each source files it under a different folder
  — and sometimes a single source files it under three.
- **The source cuts delivery mid-stream.** The film stops halfway and the player jumps back to
  the beginning. It's the most common failure, and the most invisible one: as far as the
  system can tell, the transfer "finished".
- **A subscription expires without warning.** The source keeps answering `200` and delivers,
  in place of every film, a two-kilobyte notice. Everything opens with zero seconds and
  nothing flags it.
- **Thousands of titles in a single folder**, because sources hand you the film but not
  its genre.
- **Every viewer is one connection to the source.** Ten people watching the same film are ten
  connections fetching the same bytes.

## What it does

**Unified catalog.** Reads M3U and Xtream sources, normalizes titles, recognizes the same film
across different sources, and presents it once — with every origin behind it.

**Delivery with failover.** The viewer never sees the source URL. If an origin fails before
the first byte, the system tries the next — and it tries *different* sources, never the same
one three times over.

**Two-tier library.** What gets watched can be stored on the machine's disk and, once it goes
cold, moved to a cloud account. A small disk becomes a staging area instead of a ceiling: what
is hot stays close, what nobody has asked for in days lives far away and cheap.

**Detection of what isn't a film.** Maintenance notices, error pages, and truncated deliveries
are recognized and discarded — on the playback path and the storage path, by the same
standard. Storing half a film is worse than storing nothing: it would then be served *instead*
of the source, which is intact.

**Genre classification.** With a free TMDB key, the system sorts untagged titles and creates
the categories on its own.

**Web panel.** Sources, catalog, categories, client credentials with quota and connection
limits, live playback, failures explained by cause, and the stored library.

**Export.** M3U playlist and an Xtream-compatible API, so clients keep the player they
already use.

## Installation

Requires **Go 1.25+** and **PostgreSQL 14+**.

```bash
git clone https://github.com/gacaciomg-source/VOD-MANEGER.git && cd VOD-MANEGER
```

```bash
sudo ./scripts/instalar.sh
```

The installer builds the binary, creates the systemd units, applies migrations, and starts the
service. The initial admin password appears in the log of the first run — write it down, it is
not shown again.

To update later:

```bash
sudo ./scripts/atualizar.sh
```

It backs up, pulls the new version, builds **before** stopping the service, swaps the binary,
and verifies the service came back — rolling back if it didn't.

## Development

One command, no database to install:

```bash
go run ./cmd/vodm-dev
```

This downloads and starts a real PostgreSQL (no Docker), generates the encryption key, applies
migrations, creates the admin user, and serves the panel at `http://localhost:8080`. The first
run downloads ~100 MB; later ones are fast. Data lives in `.vodm-dev/` and survives restarts.

User `admin`, password `admin-desenvolvimento`.

```bash
go test ./...
```

The integration tests spin up their own Postgres and actually execute the queries — several of
them exist because a query that compiled did not run.

## Configuration

Copy `.env.example` to `.env` and adjust. The essential ones:

| Variable | What it is |
|---|---|
| `VODM_DATABASE_URL` | PostgreSQL connection string |
| `VODM_ENCRYPTION_KEY` | Master key for stored credentials. Generate with `go run ./cmd/vodmanager genkey` |
| `VODM_PUBLIC_BASE_URL` | The address the outside world uses to reach this server |
| `VODM_ARMAZENAMENTO_LOCAL` | Folder for the on-disk library |

Everything else — cache, limits, TMDB — is configured from the panel, without a restart.

## How it's built

Go with no web framework, PostgreSQL with no ORM, a panel in plain JavaScript with no build
step. That's deliberate: the system runs on a modest VPS next to its own database, and every
extra dependency is one more thing that can break on a server nobody is watching.

Comments explain **why**, not what. Most of them record a defect that actually happened — the
most honest form of documentation there is, and what keeps the same bug from coming back
through a different path.

`docs/` holds the architecture decisions and the data model (in Portuguese).

## License

[AGPL-3.0](LICENSE).

In short: you may use, modify, and run this in your own business, commercially included. What
the license requires is that **anyone who modifies the system and offers it as a service must
publish those modifications**. Improvements flow back to the people using it, instead of
becoming someone's closed product.

## Notice

This software does not provide, host, or distribute content. It connects to sources configured
by the administrator of each installation, and the responsibility for holding the right to
access and redistribute that content rests entirely with whoever operates it.
