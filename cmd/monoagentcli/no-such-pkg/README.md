# no-such-pkg

Browser automation package `no-such-pkg` for https://example.com/.

Scaffolded from the `basic` template: one read-only action and one fragment.

## Actions

- `get_page_title` (read) — open the start page, close a consent banner if
  one shows (fragment `dismiss_banner`), and return the main heading as
  `heading`.

## Next steps

1. Point the selectors in `selectors.json` at your site (keep the best
   candidate first; `aria` candidates survive layout changes best).
2. Add an action: create `actions/<name>.json` and list `<name>` in
   `automation.json` → `actions`. Add every step type it uses to
   `permissions.steps`.
3. `monoagentcli automation validate .` then `monoagentcli automation install . --yes`.
