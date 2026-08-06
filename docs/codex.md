# Use Prism with Codex

## 1. Install and sign in

```sh
brew install circlesac/tap/crcl circlesac/tap/prism
crcl login
prism chatgpt auth login
```

Run `prism chatgpt auth login` again for each additional ChatGPT account.

## 2. Configure Codex

Find the `crcl` executable:

```sh
command -v crcl
```

Add the following to `~/.codex/config.toml`. Replace `/opt/homebrew/bin/crcl`
with the path printed by the command above.

```toml
model = "gpt-5.6"
model_provider = "prism"
model_reasoning_effort = "xhigh"

[model_providers.prism]
name = "Prism"
base_url = "https://prism.circles.ac/v1"
wire_api = "responses"

[model_providers.prism.auth]
command = "/opt/homebrew/bin/crcl"
args = ["auth", "token"]
```

Open a new Codex task after changing the configuration. The CLI can be started
normally:

```sh
codex
```

## 3. Verify

```sh
codex exec --ephemeral 'Do not use any tools. Reply with exactly PRISM_OK.'
prism chatgpt usage
```
