"""Pack PNGs into a .ico container.

Written by hand rather than with Pillow, because the only thing needed is the
container: an ICONDIR header, one ICONDIRENTRY per image, then the PNG bytes
verbatim. PNG-encoded ICO entries are understood by every browser still worth
supporting, and by Googlebot.
"""
import struct
import sys

def main(pngs, out):
    entries, offset = [], 6 + 16 * len(pngs)
    blobs = []
    for path in pngs:
        data = open(path, "rb").read()
        # Width and height live at bytes 16..24 of the IHDR chunk.
        w, h = struct.unpack(">II", data[16:24])
        # 256 is encoded as 0 in an ICO directory entry.
        entries.append(struct.pack(
            "<BBBBHHII",
            w % 256, h % 256, 0, 0, 1, 32, len(data), offset))
        blobs.append(data)
        offset += len(data)
    with open(out, "wb") as f:
        f.write(struct.pack("<HHH", 0, 1, len(pngs)))
        for e in entries:
            f.write(e)
        for b in blobs:
            f.write(b)

if __name__ == "__main__":
    main(sys.argv[1:-1], sys.argv[-1])
