#!/usr/bin/env python3
"""Render an actual transcript to PNG; never synthesize command output."""
from PIL import Image, ImageDraw, ImageFont
import pathlib
import sys
import textwrap

lines = []
for line in pathlib.Path(sys.argv[1]).read_text().splitlines():
    lines.extend(textwrap.wrap(line, 100, replace_whitespace=False, drop_whitespace=False) or [''])
font = ImageFont.truetype('/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf', 20)
image = Image.new('RGB', (1240, 40 + 28 * len(lines)), '#1e1e2e')
draw = ImageDraw.Draw(image)
for i, line in enumerate(lines):
    draw.text((20, 20 + i * 28), line, font=font, fill='#cdd6f4')
image.save(sys.argv[2])
