# Copilot Proxy

A proxy server to expose OpenAI-compatible endpoints for GitHub Copilot API

## Features

- Fetches and refreshes GitHub Copilot API tokens using a GitHub OAuth token.
- Reads GitHub Copilot OAuth token automatically from `~/.config/github-copilot/apps.json` if not passed on the command line.
- Optional access token to restrict API usage.

## Usage

## Requirements

- Valid Copilot-enabled GitHub OAuth token
- Github Copilot `oauth_token` or a valid `~/.config/github-copilot/apps.json` file

### Build

```bash
go build -o copilot-proxy main.go
```

### Run

```bash
# binary
./copilot-proxy -oauth-token <GITHUB_OAUTH_TOKEN> -access-token <randome token> [flags...]

# docker
docker run -d \
    --name copilot-proxy \
    --restart always \
    -v $HOME/.config/github-copilot:/.config/github-copilot \
    -p 8080:8080 \
    ghcr.io/xuyuanp/copilot-proxy
```

Supported flags:

- `-oauth-token` — GitHub Copilot OAuth token (will try to read from file if omitted)
- `-access-token` — (optional) Access token for user authentication to the proxy itself
- `-addr` — Address to listen on (default: `:8080`)
- `-base-path` — Base API path to match and remove from incoming requests (default: `/api/v1`)

## Examples:

### `curl`

```bash
curl -H "Authorization: Bearer <your-token>" \
    http://localhost:8080/api/v1/chat/completions \
    -XPOST \
    -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "tell me a joke"}]}' | jq '.choices[0].message.content'
```

### `openai cli`

```bash
export OPENAI_BASE_URL=http://localhost:8080/api/v1
export OPENAI_API_KEY='<your-token>'
openai api chat.completions.create -m gpt-4o --stream -g 'user' 'tell me a joke'
```

# FAQ

## Q: Where can I find my GitHub Copilot OAuth token?

In the file `~/.config/github-copilot/apps.json`, you can find your GitHub Copilot OAuth token under the key `oauth_token`. An example of the file is:

```json
{
  "github.com:xxx": {
    "user": "<your-user-name>",
    "oauth_token": "<THIS IS YOUR OAUTH TOKEN>",
    "githubAppId": "xxx"
  }
}
```

## Q: What if I don't have the file `~/.config/github-copilot/apps.json`?

1. If you have [Zed](https://zed.dev/) installed, you can sing in to GitHub Copilot in Zed.
2. If you are familiar with Neovim, you can either install [copilot.vim](https://github.com/github/copilot.vim) or [copilot.lua](https://github.com/zbirenbaum/copilot.lua) to sign in to GitHub Copilot.
3. You still need to install Neovim (>=0.11.0) and [copilot-language-server](https://github.com/github/copilot-language-server-release), run command `nvim -n --headless -u nvim/init.lua tmp.c` and follow the instructions.
