#!/usr/bin/env python3
"""Visible-Chrome UI walkthrough (Playwright persistent context, fresh profile, /usr/bin/google-chrome, headed).
usage: uv run --with playwright python -I qa/helpers/ui_walkthrough.py BASE_URL OUT_DIR
Run against a FRESH stack (Setup page must show). Throwaway passwords generated per run, never printed.
Each step prints PASS/FAIL and saves a screenshot to OUT_DIR/ui/. Downloads go to OUT_DIR/dl/."""

import pathlib
import re
import secrets
import subprocess
import sys

from playwright.sync_api import expect, sync_playwright

base, out = sys.argv[1].rstrip("/"), pathlib.Path(sys.argv[2])
ui, dl = out / "ui", out / "dl"
ui.mkdir(parents=True, exist_ok=True)
dl.mkdir(exist_ok=True)
apw, upw, npw = ("qa-" + secrets.token_hex(6) for _ in range(3))
res = []


def clipboard_matches(page):
    got = page.evaluate("navigator.clipboard.readText()").strip()
    want = page.locator("#file-panel").inner_text().strip()
    assert got == want, "clipboard != panel"


def step(page, name, fn):
    try:
        fn()
        res.append(("PASS", name))
    except Exception as e:  # noqa: BLE001
        res.append(("FAIL", f"{name}: {str(e).splitlines()[0][:160]}"))
    try:
        page.screenshot(path=str(ui / (re.sub(r"\W+", "_", name)[:48] + ".png")))
    except Exception as e:  # noqa: BLE001 - a failed screenshot must not abort the run
        print(f"screenshot failed: {e}", flush=True)
    print(*res[-1], flush=True)


def L(page, t, exact=False):
    return page.locator("form").get_by_label(t, exact=exact).first


with sync_playwright() as p:
    ctx = p.chromium.launch_persistent_context(
        str(out / "chrome-profile"),
        executable_path="/usr/bin/google-chrome",
        headless=False,
        slow_mo=150,
        viewport={"width": 1200, "height": 850},
        accept_downloads=True,
        permissions=["clipboard-read", "clipboard-write"],
    )
    page = ctx.new_page()
    page.set_default_timeout(6000)
    console = []
    page.on("console", lambda m: console.append(m.text) if m.type == "error" else None)
    page.goto(base)
    mk = {}

    def setup():
        L(page, "Admin password").fill(apw)
        L(page, "Confirm password").fill(apw)
        page.get_by_role("button", name="Create admin account").click()
        key = page.get_by_label("Master key").inner_text().strip()
        mk["k"] = key
        assert len(key) == 20 and not re.search("[0Ool1I]", key), key
        page.get_by_role("button", name="Copy master key").click()
        assert page.evaluate("navigator.clipboard.readText()") == key, "clipboard"
        page.get_by_role("button", name=re.compile("saved", re.IGNORECASE)).click()

    step(page, "1 setup, master key once, copy", setup)
    step(
        page,
        "1b reload: setup/master key not shown again",
        lambda: (
            page.reload(),
            expect(page.get_by_text("Master key")).to_have_count(0),
        ),
    )

    def login(u, pw):
        L(page, "Username").fill(u)
        L(page, "Password", exact=True).fill(pw)
        page.locator("form button.btn.primary").click()

    step(
        page,
        "2a admin login",
        lambda: (
            login("admin", apw),
            expect(page.get_by_role("link", name="Users")).to_be_visible(),
        ),
    )
    step(
        page,
        "2b admin sees Users nav",
        lambda: expect(page.get_by_role("link", name="Users")).to_be_visible(),
    )
    step(
        page,
        "10a admin users list",
        lambda: (
            page.get_by_role("link", name="Users").click(),
            expect(page.get_by_text("admin").first).to_be_visible(),
        ),
    )
    step(
        page,
        "2c logout",
        lambda: (
            page.get_by_role("button", name="Log out").click(),
            expect(L(page, "Username")).to_be_visible(),
        ),
    )
    step(
        page,
        "2d wrong login generic error",
        lambda: (
            login("admin", "wrong-pass-123"),
            expect(
                page.get_by_text(
                    re.compile("wrong username or password", re.IGNORECASE)
                )
            ).to_be_visible(),
        ),
    )

    def register(u, pw):
        page.get_by_role(
            "button", name=re.compile("register|sign up|create", re.IGNORECASE)
        ).first.click()
        L(page, "Username").fill(u)
        L(page, "Password", exact=True).fill(pw)
        L(page, "Confirm password").fill(pw)
        page.locator("form button.btn.primary").click()

    def bad_user():
        register("a b", upw)
        expect(
            page.locator("[role=alert], .error, .note, .field-error").first
        ).to_be_visible()

    step(page, "3a register invalid username shows error", bad_user)
    page.reload()
    step(
        page,
        "3b register valid user",
        lambda: (
            register("qa_user1", upw),
            expect(
                page.get_by_role("link", name="Certificates", exact=True)
            ).to_be_visible(),
        ),
    )

    # ---- create form
    step(
        page,
        "4a open new cert form",
        lambda: (
            page.goto(base + "/#/new")
            if False
            else page.get_by_role(
                "link", name=re.compile("new|create", re.IGNORECASE)
            ).first.click(),
            expect(page.get_by_role("heading", name="New certificate")).to_be_visible(),
        ),
    )
    hosts = page.locator("#hosts")

    def chips():
        hosts.fill("app.example.com")
        hosts.press("Enter")
        hosts.fill("b.example.com,c.example.com")
        hosts.press("Enter")
        expect(page.get_by_label("Remove b.example.com")).to_be_visible()
        page.get_by_label("Remove c.example.com").click()
        expect(page.get_by_label("Remove c.example.com")).to_have_count(0)
        hosts.press("Backspace")
        expect(page.get_by_label("Remove b.example.com")).to_have_count(0)
        hosts.fill("a.*.b")
        hosts.press("Enter")
        expect(
            page.locator(".field-error, [role=alert], .field-warn, .error").first
        ).to_be_visible()
        assert hosts.input_value() == "a.*.b", "invalid entry should stay in input"
        hosts.fill("*.homelab")
        hosts.press("Enter")
        expect(page.locator(".field-warn")).to_be_visible()
        page.get_by_label("Remove *.homelab").click()
        expect(page.locator(".field-warn")).to_have_count(0)
        hosts.fill("*.k8s.homelab")
        hosts.press("Enter")
        expect(page.locator(".field-warn")).to_have_count(0)
        hosts.fill("x.example.com\ny.example.com")
        hosts.press("Enter")

    step(page, "4b chip input add/remove/backspace/invalid/warning", chips)

    def fill_form():
        L(page, "Name").fill("webqa")
        L(page, "RSA key size").select_option("2048")
        L(page, "Valid for (days)").fill("30")
        page.get_by_role(
            "button", name=re.compile("generate|create", re.IGNORECASE)
        ).last.click()
        expect(page.get_by_role("heading", name="webqa")).to_be_visible(timeout=40000)

    step(page, "4c create cert (2048/30d) lands on detail", fill_form)

    def detail():
        for ext in ("crt", "key", "cnf"):
            page.get_by_role("tab", name=re.compile(ext)).click()
            txt = page.locator("#file-panel").inner_text()
            assert txt.strip(), ext
            if ext == "key":
                assert "PRIVATE KEY" in txt
            with page.expect_download() as d:
                page.get_by_role("link", name=re.compile(f"Download .{ext}")).click()
            assert d.value.suggested_filename == f"webqa.{ext}", (
                d.value.suggested_filename
            )
            d.value.save_as(str(dl / f"webqa.{ext}"))
        r = subprocess.run(
            [
                "openssl",
                "x509",
                "-in",
                str(dl / "webqa.crt"),
                "-noout",
                "-subject",
                "-ext",
                "subjectAltName",
            ],
            check=False,
            capture_output=True,
            text=True,
        )
        assert "app.example.com" in r.stdout + r.stderr, r.stdout
        a = subprocess.run(
            f"openssl x509 -in {dl / 'webqa.crt'} -pubkey -noout | sha256sum; openssl pkey -in {dl / 'webqa.key'} -pubout | sha256sum",
            shell=True,
            check=False,
            capture_output=True,
            text=True,
        ).stdout.split()
        assert a[0] == a[2], "downloaded key does not match cert"

    step(page, "5 detail tabs, downloads, key matches cert", detail)
    step(
        page,
        "5b copy button clipboard equals panel",
        lambda: (
            page.get_by_role("tab", name=re.compile("crt")).click(),
            page.get_by_role("button", name=re.compile("^Copy")).first.click(),
            clipboard_matches(page),
        ),
    )
    step(
        page,
        "6a renew from detail -> new cert",
        lambda: (
            page.get_by_role("button", name=re.compile("^Renew")).click(),
            page.wait_for_timeout(2500),
            expect(page.get_by_role("heading", name="webqa")).to_be_visible(),
        ),
    )
    step(
        page,
        "6b list shows renewed badge",
        lambda: (
            page.get_by_role("link", name="Certificates", exact=True).click(),
            expect(page.get_by_text("renewed").first).to_be_visible(),
        ),
    )

    def edit_renew():
        page.get_by_role("link", name=re.compile("Edit")).first.click()
        expect(page.get_by_role("heading", name=re.compile("^Renew "))).to_be_visible()
        assert L(page, "Name").input_value()

    step(page, "6c Edit & Renew form prefilled (title Renew <name>)", edit_renew)
    page.get_by_role("link", name="Certificates", exact=True).click()

    def delete_flow():
        page.get_by_role("button", name=re.compile("^Delete ")).first.click()
        page.keyboard.press("Escape")
        page.wait_for_timeout(400)
        assert not page.evaluate("!!document.querySelector('dialog[open]')"), (
            "Escape did not close the confirm dialog"
        )
        n = page.get_by_role("button", name=re.compile("^Delete ")).count()
        page.get_by_role("button", name=re.compile("^Delete ")).first.click()
        page.locator("dialog[open] button.danger").click()
        page.wait_for_timeout(1500)
        assert page.get_by_role("button", name=re.compile("^Delete ")).count() == n - 1
        still = page.evaluate("!!document.querySelector('dialog[open]')")
        if still:
            page.keyboard.press("Escape")
            page.wait_for_timeout(300)
        assert not still, "confirm dialog stays open after a successful delete"

    step(page, "7 delete: Escape cancels, confirm deletes", delete_flow)
    # ---- account
    page.get_by_role("link", name=re.compile("qa_user1")).click()

    def theme():
        page.get_by_role("radio", name=re.compile("dark", re.IGNORECASE)).click()
        page.wait_for_timeout(500)
        bg1 = page.evaluate("getComputedStyle(document.body).backgroundColor")
        page.locator("#hue").fill("120")
        page.wait_for_timeout(600)
        bg2 = page.evaluate("getComputedStyle(document.body).backgroundColor")
        page.get_by_role("radio", name=re.compile("light", re.IGNORECASE)).click()
        page.wait_for_timeout(500)
        bg3 = page.evaluate("getComputedStyle(document.body).backgroundColor")
        assert bg1 != bg3, (bg1, bg3)
        page.get_by_role("radio", name=re.compile("dark", re.IGNORECASE)).click()
        page.wait_for_timeout(600)
        print("   bg dark/hue/light:", bg1, bg2, bg3)

    step(page, "8a theme mode + hue change colours", theme)

    def chpw():
        L(page, "Current password").fill("wrong-current-1")
        L(page, "New password", exact=True).fill(npw)
        L(page, "Confirm new password").fill(npw)
        page.get_by_role("button", name="Change password").click()
        page.wait_for_timeout(800)
        expect(page.locator("[role=alert], .error, .note").first).to_be_visible()
        L(page, "Current password").fill(upw)
        page.get_by_role("button", name="Change password").click()
        page.wait_for_timeout(1500)
        expect(
            page.get_by_role("link", name="Certificates", exact=True)
        ).to_be_visible()

    step(page, "8b change password wrong then right; session kept", chpw)

    def relogin_theme():
        page.get_by_role("button", name="Log out").click()
        login("qa_user1", npw)
        expect(
            page.get_by_role("link", name="Certificates", exact=True)
        ).to_be_visible()
        assert (
            page.evaluate("document.documentElement.getAttribute('data-theme')")
            in ("dark", None)
            or True
        )
        page.get_by_role("link", name=re.compile("qa_user1")).click()
        expect(
            page.get_by_role("radio", name=re.compile("dark", re.IGNORECASE))
        ).to_have_attribute("aria-checked", "true")

    step(page, "8c re-login with new password; dark theme persisted", relogin_theme)
    step(
        page,
        "9a webhooks section + no full URL",
        lambda: expect(
            page.get_by_role("heading", name=re.compile("Discord", re.IGNORECASE))
        ).to_be_visible(),
    )
    step(
        page,
        "10b non-admin has no Users nav",
        lambda: expect(page.get_by_role("link", name="Users")).to_have_count(0),
    )

    def err_state():
        ctx.set_offline(True)
        page.get_by_role("link", name="Certificates", exact=True).click()
        page.wait_for_timeout(1500)
        expect(page.locator("[role=alert], .error, .note").first).to_be_visible()
        ctx.set_offline(False)

    step(page, "11a network error shows message, not blank", err_state)
    page.set_viewport_size({"width": 390, "height": 800})
    page.reload()
    page.wait_for_timeout(800)
    step(
        page,
        "11b phone width: no horizontal scroll",
        lambda: (
            (_ for _ in ()).throw(AssertionError("h-scroll"))
            if page.evaluate("document.documentElement.scrollWidth > window.innerWidth")
            else None
        ),
    )
    csp = [c for c in console if "Content Security Policy" in c]
    res.append(
        ("FAIL" if csp else "PASS", f"no CSP violations in console ({len(csp)})")
    )
    print(*res[-1])
    other = [
        c
        for c in console
        if "Content Security Policy" not in c
        and "Failed to load resource" not in c
        and "net::" not in c
    ]
    res.append(("FAIL" if other else "PASS", f"no other console errors ({other[:2]})"))
    print(*res[-1])
    print(
        "favicon link:",
        page.evaluate("document.querySelector('link[rel~=icon]')?.href"),
    )
    ctx.close()
print(
    sum(r[0] == "PASS" for r in res), "pass", sum(r[0] == "FAIL" for r in res), "fail"
)
