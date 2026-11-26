#!/usr/bin/env python3
"""Render an actual transcript to PNG; never synthesize command output."""
from PIL import Image, ImageDraw, ImageFont
import pathlib
import sys
import textwrap
import argparse
p=argparse.ArgumentParser();p.add_argument("input",type=pathlib.Path);p.add_argument("output",type=pathlib.Path);p.add_argument("--last-lines",type=int,default=30);a=p.parse_args()
assert a.last_lines>0

lines = []
for line in a.input.read_text().splitlines():
    lines.extend(textwrap.wrap(line, 100, replace_whitespace=False, drop_whitespace=False) or [''])
lines=lines[-a.last_lines:]
font = ImageFont.truetype('/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf', 20)
image = Image.new('RGB', (1240, 40 + 28 * len(lines)), '#1e1e2e')
draw = ImageDraw.Draw(image)
for i, line in enumerate(lines):
    draw.text((20, 20 + i * 28), line, font=font, fill='#cdd6f4')
image.save(a.output)
