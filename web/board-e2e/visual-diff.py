#!/usr/bin/env python3
"""Pixel diff of two DASHBOARD_SHOTS directories (base vs head); exit 1 on any difference.

  DASHBOARD_SHOTS=/tmp/base pnpm exec playwright test -c board-e2e/playwright.config.ts dashboard   # on base
  DASHBOARD_SHOTS=/tmp/head pnpm exec playwright test -c board-e2e/playwright.config.ts dashboard   # on head
  python3 board-e2e/visual-diff.py /tmp/base /tmp/head

A channel value off by 1 of 255 is dithering of the gradients and shows between two runs of the same
build (main vs main), so it is not counted; anything larger is a difference.
"""
import os, sys
from PIL import Image, ImageChops

base, head = sys.argv[1:3]
names = sorted(f for f in os.listdir(head) if f.endswith(".png"))
if not names:
    sys.exit("no screenshots in " + head)
bad = 0
for name in names:
    a, b = (Image.open(os.path.join(d, name)).convert("RGB") for d in (head, base))
    diff = ImageChops.difference(a, b).point(lambda v: 255 if v > 1 else 0).getbbox() if a.size == b.size else a.size
    print(f"{name}: {'identical' if diff is None else 'DIFFERS ' + str(diff)}")
    bad += diff is not None
sys.exit(1 if bad else 0)
