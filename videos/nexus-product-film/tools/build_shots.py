"""Generate the product scenes that are built from real Nexus captures.

Each scene is a viewport over one or more real screenshots (assets/shots/).
A camera pans and zooms over the image; spotlight boxes point at the real UI.
Run: python tools/build_shots.py  (writes compositions/<scene>.html)
"""
import json
import os

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

HEAD = """<template>
  <style>
    #root { position: absolute; inset: 0; overflow: hidden; font-family: "Inter", sans-serif; color: #fff; }
    #root .vp { position: absolute; overflow: hidden; border-radius: 16px; background: #000; border: 1px solid #2c2c2c; box-shadow: 0 40px 120px rgba(0, 0, 0, 0.6); }
    #root .cam { position: absolute; left: 0; top: 0; transform-origin: 0 0; }
    #root .cam img { display: block; }
    #root .hl { position: absolute; border-radius: 12px; border: 3px solid #e8bb67; box-shadow: 0 0 0 4000px rgba(0, 0, 0, 0.5), 0 0 40px rgba(232, 187, 103, 0.45); }
    #root .cursor { position: absolute; left: 0; top: 0; width: 46px; height: 62px; }
    %(css)s
  </style>
  <div id="root" data-composition-id="%(id)s" data-width="1920" data-height="1080">
%(body)s
  </div>
  <script>
    (function () {
      const tl = gsap.timeline({ paused: true });
      const VW = %(vw)d, VH = %(vh)d;
      const SH = %(shots)s;
      const clampXY = (sh, cx, cy, s) => {
        let x = VW / 2 - cx * s, y = VH / 2 - cy * s;
        x = Math.min(0, Math.max(VW - sh.w * s, x));
        y = Math.min(0, Math.max(VH - sh.h * s, y));
        if (sh.w * s < VW) x = (VW - sh.w * s) / 2;
        if (sh.h * s < VH) y = (VH - sh.h * s) / 2;
        return { x, y, scale: s };
      };
      const camSet = (k, cx, cy, s) => gsap.set("#" + SH[k].id, clampXY(SH[k], cx, cy, s));
      const cam = (k, at, cx, cy, s, d, ease) => tl.to("#" + SH[k].id, Object.assign(clampXY(SH[k], cx, cy, s), { duration: d, ease: ease || "power3.inOut" }), at);
      const hl = (id, at, hold) => {
        tl.fromTo("#" + id, { opacity: 0, scale: 1.06 }, { opacity: 1, scale: 1, duration: 0.3, ease: "power3.out" }, at);
        tl.to("#" + id, { opacity: 0, duration: 0.25 }, at + hold);
      };
      const show = (k, at, d) => tl.fromTo("#" + SH[k].id, { opacity: 0 }, { opacity: 1, duration: d || 0.35 }, at);
      const hide = (k, at, d) => tl.to("#" + SH[k].id, { opacity: 0, duration: d || 0.35 }, at);
%(ops)s
      window.__timelines["%(id)s"] = tl;
    })();
  </script>
</template>
"""

CURSOR = '<svg id="%s" class="cursor" viewBox="0 0 46 62"><path d="M4 3 L4 50 L16 39 L24 58 L32 54 L24 36 L40 36 Z" fill="#fff" stroke="#111" stroke-width="3" stroke-linejoin="round" /></svg>'


def scene(sid, vp, shots, hls, ops, extra_body="", css="", cursor_in=None):
    vx, vy, vw, vh = vp
    lines = ['    <div id="%s-vp" class="vp" style="left: %dpx; top: %dpx; width: %dpx; height: %dpx">' % (sid, vx, vy, vw, vh)]
    meta = {}
    for k, (src, w, h) in shots.items():
        cid = "%s-%s" % (sid, k)
        meta[k] = {"id": cid, "w": w, "h": h}
        lines.append('      <div id="%s" class="cam" style="width: %dpx; height: %dpx">' % (cid, w, h))
        lines.append('        <img src="assets/shots/%s" width="%d" height="%d" alt="" />' % (src, w, h))
        for hid, (hk, x, y, ww, hh) in hls.items():
            if hk == k:
                lines.append('        <div id="%s-%s" class="hl" style="left: %dpx; top: %dpx; width: %dpx; height: %dpx; opacity: 0"></div>' % (sid, hid, x, y, ww, hh))
        if cursor_in == k:
            lines.append("        " + CURSOR % (sid + "-cur"))
        lines.append("      </div>")
    lines.append("    </div>")
    body = "\n".join(lines) + "\n" + extra_body
    js = "\n".join("      " + o for o in ops.strip().splitlines())
    html = HEAD % {"id": sid, "body": body, "vw": vw, "vh": vh, "shots": json.dumps(meta), "ops": js, "css": css}
    with open(os.path.join(ROOT, "compositions", sid + ".html"), "w", encoding="utf-8", newline="\n") as f:
        f.write(html)


D = (1800, 1080)
VP = (120, 176, 1680, 850)

# Chat: real chat, then the expanded work log with a sub-agent run.
scene(
    "chat", VP,
    {"a": ("chat-dark.png",) + D, "b": ("chat-worklog-subagent-dark.png",) + D},
    {"pick": ("a", 862, 1000, 368, 52), "side": ("a", 22, 120, 346, 715),
     "log": ("b", 612, 246, 905, 520), "sub": ("b", 640, 436, 875, 160)},
    """
camSet("a", 900, 520, 0.9333);
camSet("b", 900, 520, 0.9333);
gsap.set("#chat-b", { opacity: 0 });
tl.fromTo("#chat-vp", { y: 200, opacity: 0, filter: "blur(16px)" }, { y: 0, opacity: 1, filter: "blur(0px)", duration: 0.6, ease: "expo.out" }, 0);
cam("a", 0.9, 1040, 990, 1.55, 0.7);
hl("chat-pick", 1.55, 1.2);
cam("a", 2.75, 195, 470, 1.25, 0.7);
hl("chat-side", 3.4, 1.3);
cam("a", 4.6, 900, 520, 0.9333, 0.5);
show("b", 5.0, 0.35);
cam("b", 5.35, 1060, 505, 1.3, 0.7);
hl("chat-log", 6.0, 1.3);
cam("b", 7.4, 1075, 515, 1.8, 0.6);
hl("chat-sub", 7.95, 1.7);
cam("b", 9.8, 1040, 760, 1.2, 0.9);
tl.to("#chat-vp", { x: -260, opacity: 0, filter: "blur(10px)", duration: 0.4, ease: "power3.in" }, 10.85);
""",
)

# Graph: the real 3D knowledge graph.
scene(
    "graph", VP,
    {"a": ("graph-3d-dark.png",) + D},
    {"node": ("a", 840, 660, 430, 250), "stale": ("a", 992, 618, 46, 46), "legend": ("a", 1385, 255, 350, 205)},
    """
camSet("a", 890, 600, 0.9333);
tl.fromTo("#graph-vp", { scale: 0.9, opacity: 0, filter: "blur(16px)" }, { scale: 1, opacity: 1, filter: "blur(0px)", duration: 0.6, ease: "expo.out" }, 0);
cam("a", 0.4, 1010, 720, 1.55, 2.4, "sine.inOut");
hl("graph-node", 2.1, 1.6);
cam("a", 3.9, 1015, 650, 1.9, 0.7);
hl("graph-stale", 4.5, 1.1);
cam("a", 5.6, 1560, 380, 1.7, 0.7);
hl("graph-legend", 6.2, 1.0);
tl.to("#graph-vp", { scale: 1.08, opacity: 0, filter: "blur(10px)", duration: 0.4, ease: "power3.in" }, 7.08);
""",
)

# Install: the real Agents page and its install button.
scene(
    "install", VP,
    {"a": ("agents-dark.png",) + D},
    {"list": ("a", 392, 455, 262, 360), "btn": ("a", 1432, 46, 242, 48)},
    """
camSet("a", 900, 470, 0.9333);
tl.fromTo("#install-vp", { y: 80, opacity: 0, filter: "blur(12px)" }, { y: 0, opacity: 1, filter: "blur(0px)", duration: 0.5, ease: "expo.out" }, 0);
cam("a", 0.35, 560, 560, 1.6, 0.7);
hl("install-list", 1.0, 1.1);
cam("a", 2.2, 1450, 200, 1.7, 0.7);
tl.fromTo("#install-cur", { x: 1200, y: 420, scale: 0.6, transformOrigin: "0 0" }, { x: 1555, y: 74, duration: 0.6, ease: "power3.out" }, 2.5);
tl.to("#install-cur", { scale: 0.5, duration: 0.06, yoyo: true, repeat: 1 }, 3.15);
hl("install-btn", 3.2, 1.9);
tl.to("#install-vp", { x: -200, opacity: 0, filter: "blur(10px)", duration: 0.38, ease: "power3.in" }, 5.2);
""",
    cursor_in="a",
)

# Teams: the real Share chat dialog.
scene(
    "teams", VP,
    {"a": ("share-dialog-dark.png",) + D},
    {"who": ("a", 578, 630, 645, 115), "code": ("a", 578, 278, 645, 46), "keep": ("a", 578, 515, 645, 100)},
    """
camSet("a", 900, 520, 0.9333);
tl.fromTo("#teams-vp", { y: 80, opacity: 0, filter: "blur(12px)" }, { y: 0, opacity: 1, filter: "blur(0px)", duration: 0.5, ease: "expo.out" }, 0);
cam("a", 0.5, 900, 560, 1.45, 0.8);
hl("teams-who", 1.4, 1.2);
hl("teams-keep", 2.6, 1.0);
cam("a", 3.7, 900, 330, 1.9, 0.7);
hl("teams-code", 4.3, 2.4);
tl.to("#teams-vp", { y: 60, opacity: 0, filter: "blur(10px)", duration: 0.38, ease: "power3.in" }, 7.08);
""",
)

# Usage: the real Settings > Usage page.
scene(
    "usage", VP,
    {"a": ("settings-usage-dark.png",) + D},
    {"cards": ("a", 686, 306, 620, 144), "agents": ("a", 686, 684, 460, 175)},
    """
camSet("a", 1000, 380, 1.45);
tl.fromTo("#usage-vp", { y: 80, opacity: 0, filter: "blur(12px)" }, { y: 0, opacity: 1, filter: "blur(0px)", duration: 0.45, ease: "expo.out" }, 0);
hl("usage-cards", 0.45, 1.1);
cam("a", 1.5, 950, 800, 1.35, 0.7);
hl("usage-agents", 2.1, 1.1);
tl.to("#usage-vp", { scale: 0.94, opacity: 0, filter: "blur(10px)", duration: 0.35, ease: "power3.in" }, 3.38);
""",
)

# Private: real redaction of pasted secrets.
PRIV_CSS = """#root .pills { position: absolute; left: 120px; top: 870px; display: flex; gap: 22px; }
    #root .pill { padding: 20px 32px; border-radius: 16px; background: #121316; border: 1px solid #2c3038; font-size: 32px; font-weight: 700; display: flex; gap: 16px; align-items: center; letter-spacing: -0.01em; }
    #root .pill i { width: 14px; height: 14px; border-radius: 50%; background: #e8bb67; display: block; }"""
PRIV_BODY = """    <div class="pills">
      <div id="private-p0" class="pill"><i></i>Native Windows app</div>
      <div id="private-p1" class="pill"><i></i>Not Electron</div>
      <div id="private-p2" class="pill"><i></i>Private by default</div>
    </div>
"""
scene(
    "private", (120, 176, 1680, 660),
    {"a": ("chat-redaction-dark.png",) + D},
    {"red": ("a", 984, 230, 310, 140)},
    """
camSet("a", 1150, 300, 1.25);
tl.fromTo("#private-vp", { y: 60, opacity: 0, filter: "blur(12px)" }, { y: 0, opacity: 1, filter: "blur(0px)", duration: 0.45, ease: "expo.out" }, 0);
cam("a", 0.3, 1180, 300, 1.75, 0.7);
hl("private-red", 0.95, 2.1);
tl.fromTo(["#private-p0", "#private-p1", "#private-p2"], { opacity: 0, y: 30 }, { opacity: 1, y: 0, duration: 0.4, ease: "power3.out", stagger: 0.12 }, 1.35);
tl.to(["#private-vp", "#private-p0", "#private-p1", "#private-p2"], { opacity: 0, filter: "blur(10px)", duration: 0.3, ease: "power2.in" }, 3.4);
""",
    extra_body=PRIV_BODY, css=PRIV_CSS,
)
print("ok")
