# getfm

Find free models across configurable providers, then check whether their
endpoints actually work.

`getfm list` fetches each provider's model catalogue and filters it down to what
is genuinely free. `getfm test` sends a minimal completion request to confirm an
endpoint answers, authenticates, and generates. `getfm` on its own opens an
interactive browser, and it takes the same flags as the subcommands, so
`getfm -providers kilocode` opens the browser with one provider in it.

```
go build -o getfm ./cmd/getfm
```

## Getting started

Build it and put it somewhere on your `PATH`:

```
make build      # optimized binary in bin/
make install    # copy into ~/.local/bin
```

Check what is wired up before trusting any of it. `getfm providers` prints one
row per provider with how many models each returned, whether a key was found,
and whether the endpoint can be probed:

```
PROVIDER      MODELS  KEY  PROBE  FREE  ENDPOINT
openrouter    464     -    yes    -     https://openrouter.ai/api/v1/models
kilocode      399     -    yes    -     https://api.kilo.ai/api/gateway/models
```

Listing free models needs no key at all, because catalogues are public. A probe
may need one, and getfm tells you when it is missing rather than failing quietly.

```
getfm list                              # every free model
getfm list -providers kilocode          # just one provider
getfm list -free zero                   # only models priced at zero
getfm test kilocode/qwen/qwen3-235b:free
```

Then open the browser, which is what most people actually want:

```
getfm
```

## Configuring providers.json

Providers are described by `providers.json`. getfm looks in the working
directory first, then your per-user config directory, so a checkout runs with no
setup and an installed binary can still find a catalog you own.

The bundled catalog has four providers. Adding your own is a JSON edit rather
than a code change, and this is the smallest entry that is useful:

```json
{
  "providers": [
    {
      "name": "example",
      "label": "Example",
      "base_url": "https://api.example.com/v1",
      "models_path": "/models",
      "mapping": { "list": "data", "id": "id" },
      "completions": {
        "path": "/chat/completions",
        "auth_header": "Authorization",
        "auth_prefix": "Bearer ",
        "body": "{\"model\":\"{{.Model}}\",\"messages\":[{\"role\":\"user\",\"content\":\"{{.Prompt}}\"}],\"max_tokens\":1}"
      }
    }
  ]
}
```

Check a catalog before relying on it. `getfm providers` will report a missing or
mistyped field at load time rather than discovering it on your first fetch:

```
getfm providers -config /path/to/providers.json
```

Keys are optional and are never written into this file. Pass one on the command
line with `-key example=sk-...`, or let getfm read the environment variables
named in `env_keys`. Both are covered in [API keys](#api-keys), and every field
is documented under [Configuration](#configuration).

## The browser

```
getfm
```

Navigation follows vim. `j`/`k` move, `/` searches, `gg` and `G` jump to the
ends, `ctrl-d` and `ctrl-u` page. Typing and movement never collide because the
browser has three modes, in the spirit of vim: normal mode handles movement and
commands, `/` drops into a search prompt that takes every keystroke until you
press enter, and the card is a modal that swallows keys while it is open.

| Key | Action |
| --- | --- |
| `j` `k` | move down and up |
| `gg` `G` | jump to top and bottom, also `home` and `end` |
| `ctrl-d` `ctrl-u` | half page down and up |
| `/` | search, `enter` to accept, `esc` to clear |
| `enter` | open the model card |
| `f` | toggle free-only and everything |
| `t` | probe the selected model |
| `T` | probe everything currently listed |
| `c` | copy the selected model id, also on `ctrl+c` |
| `r` | refresh the catalogues |
| `S` | refresh and remember the catalogues |
| `q` | quit, also `ctrl+q` |

`ctrl+c` copies rather than quitting, so `ctrl+q` was added alongside `q`. In
the search prompt and in the model card `ctrl+c` still cancels, because there it
means "abandon what I was typing" rather than "act on the selected row".

Copying needs a clipboard tool on the host. On Linux that is `xclip`, `xsel` or
`wl-clipboard`, depending on the display server; without one the browser says so
in the status row instead of failing silently.

## What is new

getfm remembers which models each provider offered last time and marks the ones
that were not in that record, so a change to a provider's catalogue is visible
rather than something you have to notice by eye. A new model is tinted and
prefixed with `+`, and the header counts them.

Press `S` to refresh and write the record. The marks last the rest of the
session: refreshing again will not erase the models the refresh just revealed,
which is the whole point of asking for a refresh. Next launch they are simply
known.

The record lives in the user cache directory, `%LocalAppData%\getfm` on Windows,
`~/Library/Caches/getfm` on macOS, `$XDG_CACHE_HOME/getfm` or `~/.cache/getfm`
elsewhere. It is regenerable, so deleting it costs you nothing but one round of
"new" marks. `-store` points it somewhere else.

A provider that fails to respond is left out of the record entirely. Otherwise a
dropped connection would make every one of its models look new next time.

## The model card

`enter` opens a card for the model under the cursor. It shows the facts first,
then the provider's own record verbatim as indented JSON, because the four
providers publish wildly different fields and a fixed subset would hide exactly
the detail that matters.

`tab` pages between the card and the last exchange:

- **model card** — name, context, price, how it qualified as free, modality,
  description, supported parameters, and the full provider record.
- **request / response** — the method, URL, headers and pretty-printed body
  getfm sent, then the status, latency, headers and body that came back.

The request view works before anything has been probed, so you can inspect
exactly what getfm would send. `enter` runs a probe from inside the modal and
the response fills in behind it.

Bodies are re-indented when they are JSON and shown verbatim when they are not.
The captured credential slot renders as `Bearer [redacted]` so you can see that
auth is being sent without the value ever entering the transcript.

The header shows every provider with its model and free counts. A provider that
failed is marked rather than dropped, so a gap is never mistaken for "no free
models".

## The model table

Models render as a single aligned row each rather than a two-line card, which
roughly doubles how many fit on screen:

```
  PROVIDER  MODEL                                          CONTEXT  $/1M IN  $/1M OUT  FREE
▌ openrouter apodex/apodex-1.1-mini:free                    262k     $0.00     $0.00     suffix
  openrouter google/gemma-4-31b-it:free                      262k     $0.00     $0.00     suffix
  kilocode   nvidia/nemotron-3.5-lightning:free              1.0M     $0.00     $0.00     suffix
```

Columns are sized to their content and then given whatever slack is left to
MODEL, the column you actually scan. When the terminal gets narrow the least
useful columns are dropped one at a time, in this order: FREE, CONTEXT,
$/1M OUT, $/1M IN, then PROVIDER. MODEL is never dropped. PROVIDER survives
longer than the rest because knowing which gateway a model sits behind matters
more here than its context window.

A price shown as `-` means the provider published no pricing at all, which is
different from a price of `$0.00`.

## Scripting

```
getfm list [flags]              list free models
getfm test [flags] [MODEL]      send a minimal completion
getfm providers [flags]         show configured providers and key status
```

`MODEL` is written `provider/model`. Because model ids contain slashes the split
is on the first one: `openrouter/qwen/qwen3.8-27b:free` is provider
`openrouter` and model `qwen/qwen3.8-27b:free`.

```bash
getfm list -providers kilocode
getfm test kilocode/kilo-auto/free
getfm test -providers kilocode -all -n 10 -c 3
```

Both take `-json`. `test` exits non-zero if any probe fails, so it works as a CI
check. Without a terminal the browser falls back to the listing.

## Deciding what counts as free

Providers advertise free access in different ways, so the rules are flags and
combine together. A `-free` value switches on exactly the rules it names:
`-free zero` is the price rule alone, `-free suffix` the id marker alone, and
`all`, the default, asks for both.

- `-free suffix` — the id ends in a marker, OpenRouter's `:free` by default.
  Change it with `-suffix`.
- `-free zero` — the provider publishes a price of zero for input and output.
- `-free none` — turn both off and select models yourself.
- `-allow` / `-deny` — comma separated lists, both accepting `*` and `?`
  wildcards, matched case-insensitively.

`-allow` beats every implicit rule and `-deny` beats everything including
`-allow`. Two things are provider declarations rather than rules, so they always
apply and are not turned off by `-free none`:

- `free_always` in the catalog, for local endpoints that cost nothing to run.
- `free_ids` in the catalog, for providers that publish no pricing and so cannot
  be detected by price.

A model whose provider publishes no pricing is never reported as free on the
strength of the zero-price rule. Unpublished pricing is not evidence of a zero
price.

## Configuration

Providers live in `providers.json`, found by searching, in order:

1. the path given to `-config`
2. `./providers.json`
3. the per-user config directory, such as `%AppData%\getfm` on Windows,
   `~/Library/Application Support/getfm` on macOS, `$XDG_CONFIG_HOME/getfm` or
   `~/.config/getfm` elsewhere
4. the directory holding the executable

The user directory is searched before the executable directory deliberately. A
system-wide install such as `C:\Program Files` is read-only, so anyone editing
their catalog needs somewhere they own. Copy the bundled catalog there to get
started.

Adding a provider is a JSON edit, not a code change.

```json
{
  "providers": [
    {
      "name": "kilocode",
      "label": "Kilo Code",
      "base_url": "https://api.kilo.ai/api/gateway",
      "models_path": "/models",
      "env_keys": ["KILOCODE_API_KEY", "KILO_API_KEY"],
      "mapping": {
        "list": "data",
        "id": "id",
        "context": "context_length",
        "prompt_price": "pricing.prompt",
        "completion_price": "pricing.completion"
      },
      "free_ids": ["some-model"],
      "completions": {
        "path": "/chat/completions",
        "auth_header": "Authorization",
        "auth_prefix": "Bearer ",
        "body": "{\"model\":\"{{.Model}}\",\"messages\":[{\"role\":\"user\",\"content\":\"{{.Prompt}}\"}],\"max_tokens\":1,\"temperature\":0}",
        "usage": "usage.total_tokens"
      }
    }
  ]
}
```

`mapping` locates the fields getfm wants. `list` is the dotted path to the array
of models; everything else resolves relative to each element. Omit a path when
a provider does not publish that field, which is how "no pricing information" is
expressed, as opposed to a price of zero. `strip_prefix` removes a prefix from an
id before it is used in a request, which Gemini needs.

`completions.body` is a JSON template rendered with `{{.Model}}` and
`{{.Prompt}}`. Values are escaped for use inside a JSON string, so a prompt
containing quotes cannot break the request. Omit `completions` for a provider
that can be listed but not probed.

`headers` is optional, and only a provider that gates its models endpoint
behind auth needs it. Each value is a template rendered with `{{.Key}}`, which
expands to the key resolved for that provider, so the credential is never
written into the file:

```json
"headers": { "Authorization": "Bearer {{.Key}}" }
```

A provider that declares no `headers` gets a models request with no credential
attached, which is what all four bundled providers expect. When a provider
resolves no key, no declared header is sent rather than one carrying an empty
credential.

Unknown fields are rejected at load time, so a typo fails loudly instead of
silently doing nothing.

## The bundled providers

Four providers ship by default, and they differ more than you would expect.

**OpenRouter** (`openrouter.ai/api`) publishes full pricing. 464 models, 21 free.
Free models carry the `:free` suffix. Probing needs `OPENROUTER_API_KEY`.

**Kilo Code** (`api.kilo.ai/api/gateway`) is OpenRouter-compatible and publishes
pricing plus an `isFree` flag. 399 models, 20 free. Its models endpoint and its
free models both work without a key: anonymous callers are rate limited to 200
requests per hour per IP. This is currently the most useful provider here, and
probing it reveals things a price column cannot. The two `google/lyria-3-*`
models are listed at $0.00 but answer `PAID_MODEL_AUTH_REQUIRED`, so a zero
price does not mean a working endpoint. That is precisely what this tool is for.

**Cline** (`api.cline.bot/api/v1`) returns only `id`, `object`, `created` and
`owned_by`, with no pricing at all. Its free models still appear, because they
keep the OpenRouter-style `:free` suffix, so the suffix rule finds 17 of them.
Cline's documentation states that free model usage is not available through the
Cline API and is limited to the IDE extension and CLI, so expect probes to fail
even though the models are listed.

**OpenCode Zen** (`opencode.ai/zen/v1`) also publishes no pricing, returning the
same four bare fields for 85 models. Its free models are declared through
`free_ids`, since only a suffix rule would miss `big-pickle`. Probing reveals
that the free tier refuses external callers: `OpenCode's free tier can only be
used from within OpenCode`. Zen also routes different model families to
different endpoints (`/responses`, `/messages`, `/chat/completions`,
`/systemone`); the bundled catalog probes `/chat/completions`, which covers most
of the free models but not all of them.

## API keys

Keys are optional. Every bundled provider lists its catalogue without one, and
Kilo Code can be probed without one too. Keys are only needed where a provider
requires them.

Resolution order is `-key provider=value` first, then the provider's `env_keys`
in order. Keys are never written to disk and never printed; `providers` reports
only which variable a key came from. Error bodies are scrubbed of anything
key-shaped before display, since some providers echo the credential back.

A key reaches the models endpoint only where a provider says so, through
`headers`; everywhere else the catalogue request stays anonymous.

`-c` defaults to 2 for probes because free tiers are usually rate limited. A 429
is reported per model rather than retried.

## Layout

```
cmd/getfm        entrypoint
internal/cli     commands, flags, output
internal/tui     interactive browser
pkg/config       providers.json schema and validation
pkg/provider     fetching and decoding catalogues
pkg/free         free-model rules
pkg/probe        minimal completion requests
```

## Platforms

Builds for Linux, macOS and Windows. The only platform-specific code is which
termination signals to listen for, split across `signals_unix.go` and
`signals_windows.go`. Everything else defers to `os.UserConfigDir` and
`os.UserCacheDir` so paths follow each platform's own convention.

The interactive browser needs a terminal with alt-screen support. On Windows
that means ConPTY, so Windows 10 version 1909 or newer, practically Windows
Terminal or a current PowerShell. Older console hosts render the browser
incorrectly; the `list`, `test` and `providers` commands are plain text and work
anywhere.

## Building

A Makefile wraps the ordinary Go commands.

```
make build      # optimized binary in bin/
make run        # build, then launch the browser
make install    # copy the binary into the per-user bin directory
make uninstall  # remove it again
make clean      # remove build and coverage artifacts
make test       # suite under the race detector
make cover      # suite with a coverage report
make vet        # go vet, the standard static checks
make staticcheck # unused code and correctness findings
make check      # fmt, vet, staticcheck and test, as CI runs them
make cross      # build the six verified platforms into bin/
make help       # list the targets
```

`make check` runs staticcheck, which is not part of the Go toolchain, so it has
to be installed once:

```
go install honnef.co/go/tools/cmd/staticcheck@latest
```

It is looked up on `PATH` and then in `$(go env GOPATH)/bin`, so the target
works either way.

The build is optimized: `-trimpath` drops local paths so the binary is
reproducible, `-ldflags "-s -w"` removes the symbol table and DWARF, and cgo is
disabled for a static binary with no libc dependency. Together they take it from
about 15 MB to 10 MB.

`make install` puts the binary in `~/.local/bin`, which is the convention most
Linux distributions already expect. macOS does not put that directory on `PATH`
by default, so the target prints the one line to add if it is missing.

Override the destination with `BINDIR`, and prefix with `DESTDIR` for a staged
install:

```
make install BINDIR=~/.local/bin
make install DESTDIR=/tmp/pkg
```

On Windows the target installs to `%LOCALAPPDATA%\Microsoft\WindowsApps`,
which is already on `PATH` for any current user and needs no administrator
rights, and the binary is named `getfm.exe`. GNU Make itself is not part of a
default Windows install, so that path assumes a shell such as Git Bash; on a
plain `cmd.exe` run `go build` directly instead.

## Tests

```
go test ./...
```

The browser's navigation, modal and layout are tested by driving the model
directly: `j`/`k`/`gg`/`G` movement, search mode, free-only toggling, card
open/close and paging, credential redaction in the request view, and assertions
that the rendered frame and the overlay both fit the terminal at several sizes.

The command line is driven through `cli.Main` with both streams captured, so
flag parsing, the three exit codes, the JSON documents and the resolution of
`provider/model` are covered without a network. Provider endpoints in the suite
are local `httptest` servers, and no test needs an API key.
## License

MIT. See [LICENSE](LICENSE).
