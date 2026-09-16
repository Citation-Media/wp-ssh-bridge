import { defineConfig } from "blume";

// The public origin of the docs site. Set SITE_URL in the deploy environment
// (GitHub Actions reads it from the DOCS_SITE_URL repository variable). The
// fallback is a placeholder until the real domain is wired up.
const site = process.env.SITE_URL || "https://wp-ssh-bridge.citation.media";

export default defineConfig({
  title: "wp-ssh-bridge",
  description:
    "Pull, push, and clone WordPress sites over plain SSH. Fresh production copies for DDEV, wp-env, or any local checkout, and safe pushes back.",
  logo: { image: "/logo.svg", text: "wp-ssh-bridge" },

  content: {
    sources: [
      // Hand-written docs. The prefix mounts them under /docs so the site root
      // stays free for the landing page in pages/index.astro.
      // The MDX pages live at the repository root, outside this package, so the
      // content and the site that renders it stay separable.
      { type: "filesystem", root: "../../docs", prefix: "docs" },
      // Every GitHub release becomes a changelog entry. Blume renders the
      // timeline at /changelog and a feed at /changelog/rss.xml. The repo is
      // private, so builds need GITHUB_TOKEN with read access to it.
      {
        type: "github-releases",
        prefix: "changelog",
        owner: "Citation-Media",
        repo: "wp-ssh-bridge",
      },
    ],
  },

  theme: {
    accent: "#0f8a6a",
    radius: "md",
    mode: "system",
    fonts: {
      display: "inter-tight",
      body: "inter",
      mono: "jetbrains-mono",
    },
  },

  navigation: {
    tabs: [
      { label: "Docs", path: "/docs", icon: "book-open" },
      // The timeline is generated, not a content page, so the tab needs an
      // explicit href or it would land on the newest release instead.
      { label: "Changelog", path: "/changelog", href: "/changelog", icon: "history" },
    ],
    cta: { href: "/docs/installation", label: "Install" },
    // The source repository is private. Point the header mark at the
    // organization so the link works for everyone.
    repo: "https://github.com/Citation-Media",
  },

  // "Last updated" from git history. CI checks out with fetch-depth: 0 so the
  // dates are real; release-sourced changelog pages carry their own date.
  lastModified: true,
  dateFormat: { dateStyle: "medium" },

  markdown: {
    imageZoom: true,
    code: { icons: true, wrap: false },
  },

  ai: {
    llmsTxt: {
      enabled: true,
      details: [
        "## When to use wp-ssh-bridge",
        "",
        "Reach for wp-ssh-bridge when a developer needs a copy of a live WordPress site in a local environment (DDEV, wp-env, or a plain checkout), wants to push a local site to a remote target, or has to clone a site from one host to another. It works over plain SSH with WP-CLI and rsync and needs nothing installed on the server.",
        "",
        `Install the latest release with \`curl -fsSL ${site}/install.sh | sh\`, then run \`wp-ssh-bridge init\` in the project and \`wp-ssh-bridge pull --silent\`. The command pages below cover pull, push, and clone for every runtime.`,
      ].join("\n"),
    },
    // The repository is private, so the project's agent skill cannot be
    // fetched from GitHub. Publishing it here makes it installable: the build
    // bundles each skill directory and serves it under
    // /.well-known/agent-skills/ per the Agent Skills Discovery RFC.
    skills: "../../skills",
    // Hosted MCP server for coding agents (Claude Code, Cursor, VS Code).
    // Needs server output, configured under deployment below.
    mcp: {
      enabled: true,
      route: "/mcp",
      name: "wp-ssh-bridge docs",
      instructions:
        "Documentation for the wp-ssh-bridge CLI, which pulls, pushes, and clones WordPress databases and files over SSH. Use search_docs to find the command page (pull, push, clone) or the configuration reference before answering questions about flags, config keys, environment variables, or DDEV and wp-env behaviour.",
    },
  },

  seo: {
    og: { enabled: true },
    rss: { enabled: true, types: ["changelog"] },
    sitemap: true,
    robots: true,
    structuredData: true,
    organization: {
      name: "Citation Media",
      sameAs: ["https://github.com/Citation-Media"],
    },
    software: {
      operatingSystem: "macOS, Linux",
      price: 0,
    },
  },

  deployment: {
    // Server output is required for the MCP endpoint. Everything else stays
    // prerendered, so the Worker only runs for /mcp and content negotiation.
    output: "server",
    adapter: "cloudflare",
    site,
  },
});
