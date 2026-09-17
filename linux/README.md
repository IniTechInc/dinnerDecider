# DinnerDecider for Linux / IoT

Photograph your fridge, a local Gemma 4 E4B identifies what is in it, and the
result drives inventory, recipes and a shopping list. Nothing leaves the box.

One static Go binary with the web UI embedded in it. Inference is the single
exception: llama.cpp is **not** compiled in. An official prebuilt `llama-server`
is supervised as a child process and reached over loopback HTTP, which is what
keeps this binary cgo-free and trivially cross-compiled for a Raspberry Pi, a
Jetson or an x86 box alike.

## Build

```sh
cd linux
go build ./cmd/dinnerdecider
```

Cross-compiling needs no toolchain, because there is no cgo:

```sh
GOOS=linux GOARCH=arm64 go build -o dist/dinnerdecider-arm64 ./cmd/dinnerdecider
GOOS=linux GOARCH=amd64 go build -o dist/dinnerdecider-amd64 ./cmd/dinnerdecider
```

Stamp a release build so `GET /api/status` and `dinnerdecider version` report
something useful:

```sh
go build -ldflags "\
  -X main.version=$(git describe --tags --always) \
  -X main.commit=$(git rev-parse --short HEAD) \
  -X main.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  ./cmd/dinnerdecider
```

## Run

```sh
dinnerdecider                      # serve, with the defaults below
dinnerdecider token show           # print the pairing token
dinnerdecider token rotate         # issue a new one (unpairs every browser)
dinnerdecider token forget         # delete it
dinnerdecider version
dinnerdecider -h                   # the full flag list
```

Defaults: bind `127.0.0.1`, HTTP `8080`, HTTPS `8443`, self-signed TLS, token
auth, state in `/var/lib/dinnerdecider`.

A quick local run against a scratch directory:

```sh
dinnerdecider --state-dir=/tmp/dd --tls=off --auth=none --port=18080
```

## Configuration

Four layers, lowest priority first:

1. built-in defaults
2. the config file (`/etc/dinnerdecider/config.json`, or `--config`)
3. `DD_`-prefixed environment variables
4. flags

`--accept-lan-exposure` is required, by flag or environment only, before an
unauthenticated service may bind a non-loopback address.

## Both listeners, and why

With TLS enabled the service listens on **both** ports, deliberately:

- `getUserMedia` is gated to secure contexts, so the **camera only works on the
  HTTPS origin**.
- A browser cannot reach that origin at all until it trusts the CA, and the CA
  is fetched from the unauthenticated `/ca.crt` route.

Serving only HTTPS makes the first visit a dead end. Serving only HTTP makes the
camera permanently unavailable. The plain listener is the bootstrap path and the
file-upload fallback.

## Demo mode

The service starts and serves its whole surface with no model installed. Scans
return sample data, and `GET /api/status` says so in `model.demoMode` and
`model.demoReason` rather than pretending. Demo mode is chosen when any of these
is true:

- there is no official `llama-server` build for this architecture
- the model weights are missing
- the vision projector (`mmproj`) is missing — weights alone cannot see a photo
- the supervisor refused the configured runtime parameters

The decision is made **once, at startup**. A model downloaded while the service
is running therefore needs a restart to take effect.

Install weights under `<state-dir>/models` (or `--models-dir`), or drive the
download from the model screen in the UI.

## Startup order

Nothing slow happens before the listeners are up. The runtime resolution can
mean a several-hundred-megabyte `llama-server` download on first boot, and a Pi
on a slow link should be serving its own UI while that happens rather than
looking dead. In order:

1. state dir, database, migrations, photo store
2. TLS material, pairing token, authenticator
3. hardware detection and the backend decision (real or demo)
4. **listeners bind**
5. photo sweeper, memory-pressure poller, runtime resolution/download

The model is never loaded at startup. Readiness means *serving*, not *warm*.

## Testing

```sh
go test ./...
```

## Known issues

- **Photos are not attached to their scan.** `photo.scanId` has a foreign key to
  the `scan` table, but nothing in production code ever inserts a `scan` row —
  only tests do. Every `AttachToScan` therefore fails with
  `FOREIGN KEY constraint failed`, which is logged and deliberately does not
  fail the request. Consequence: photos are not swept promptly when their scan
  is evicted; they are cleaned up by the retention sweep instead (24 h by
  default), so the leak is bounded but the designed cleanup path is dead. The
  fix is a design call — persist scans, drop the unused `scan`/`scan_item`
  tables, or relax the constraint — and is not made here.
