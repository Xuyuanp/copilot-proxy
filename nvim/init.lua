local apps_json = vim.fn.globpath(vim.env.HOME, ".config/github-copilot/apps.json")
if vim.fn.filereadable(apps_json) == 1 then
	print("You are already signed in to GitHub.\n")
	vim.cmd("qall")
end

-- https://github.com/github/copilot-language-server-release

local methods = {
	signIn = "signIn",
}

---@param client_id integer
---@param bufnr integer
local function sign_in(client_id, bufnr)
	if vim.g.copilot_lsp_signin_pending then
		return
	end

	vim.g.copilot_lsp_signin_pending = true

	local client = vim.lsp.get_client_by_id(client_id)
	if not client then
		return
	end
	for _, req in pairs(client.requests) do
		if req.type == "pending" and req.method == methods.signIn then
			return
		end
	end
	client:request(methods.signIn, vim.empty_dict(), nil, bufnr)
end

---@type table<string, lsp.Handler>
local handlers = {
	---@param res {busy: boolean, kind: 'Normal'|'Error'|'Warning'|'Inactive', message: string}
	didChangeStatus = function(_err, res, ctx)
		if res.busy then
			return
		end

		if res.kind == "Normal" and not res.message then
			return
		end

		-- message: You are not signed into GitHub. Please sign in to use Copilot.
		if res.kind == "Error" and res.message:find("not signed into") then
			sign_in(ctx.client_id, ctx.bufnr)
			return
		end
	end,

	---@param res {command: lsp.Command, userCode: string, verificationUri: string}
	signIn = function(err, res, ctx)
		vim.g.copilot_lsp_signin_pending = nil

		if err then
			print("Failed to call signin method: " .. vim.inspect(err))
			return
		end

		local client = vim.lsp.get_client_by_id(ctx.client_id)
		if not client then
			return
		end

		print("First copy your one-time code: " .. res.userCode)
		print("\nPress enter to continue...")
		local _ = io.read()

		print("Then open your browser and visit: " .. res.verificationUri .. " if it does not open automatically.\n")

		print("Checking...\n")

		client:exec_cmd(res.command, { bufnr = ctx.bufnr }, function(err, res)
			if err then
				print("Failed to call signin command: " .. vim.inspect(err))
				vim.cmd("qall")
				return
			end
			if res.status == "OK" then
				print("Successfully signed in as: " .. res.user)
			else
				print("Failed to sign in: " .. vim.inspect(res))
			end

			vim.cmd("qall")
		end)
	end,
}

local version = vim.version()

vim.lsp.config("copilot-ls", {
	cmd = { "copilot-language-server", "--stdio" },
	root_dir = vim.uv.cwd(),
	init_options = {
		editorInfo = {
			name = "Neovim",
			version = string.format("v%d.%d.%d", version.major, version.minor, version.patch),
		},
		editorPluginInfo = {
			name = "copilot-ls",
			version = "dev",
		},
	},
	handlers = handlers,
})

vim.lsp.enable("copilot-ls")
