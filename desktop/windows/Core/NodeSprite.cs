#nullable enable
using System.IO.Compression;

namespace Nexus.Core;

/// <summary>
/// Bakes the node images for the 3D graph (pure C#, no Win2D): a lit ball,
/// rounded square or diamond in the kind's colour, with a soft glow around
/// it and an optional stale ring, as straight-alpha RGBA. The body fills the
/// middle half of the image (radius = size / 4); the glow fills the rest.
/// Each image is drawn once per theme and shared by every node of its kind,
/// so the GPU only scales and fades textures each frame.
/// </summary>
internal static class NodeSprite
{
    /// <summary>Body radius as a fraction of the image size.</summary>
    public const double BodyFraction = 0.25;

    public static byte[] Render(int size, int kind, (byte R, byte G, byte B) color, bool dark, (byte R, byte G, byte B)? staleRing)
    {
        var rgba = new byte[size * size * 4];
        var c = size / 2.0;
        var body = size * BodyFraction;
        var px = 1.0 / body; // one pixel in body units, for anti-aliasing
        var glowAlpha = dark ? 0.42 : 0.26;
        // Light from the upper left, towards the viewer.
        double lx = -0.42, ly = -0.55, lz = 0.72;
        var ll = Math.Sqrt(lx * lx + ly * ly + lz * lz);
        lx /= ll; ly /= ll; lz /= ll;
        double hx = lx, hy = ly, hz = lz + 1;
        var hl = Math.Sqrt(hx * hx + hy * hy + hz * hz);
        hx /= hl; hy /= hl; hz /= hl;
        for (var y = 0; y < size; y++)
        {
            for (var x = 0; x < size; x++)
            {
                // Body units: the body edge is at distance 1.
                var u = (x + 0.5 - c) / body;
                var v = (y + 0.5 - c) / body;
                var d = ShapeDistance(kind, u, v); // ≤ 1 inside
                double r = 0, g = 0, b = 0, a = 0;

                // Glow: fades from the body edge out to twice its radius.
                var radial = Math.Sqrt(u * u + v * v);
                if (radial < 2)
                {
                    var t = Math.Clamp((2 - Math.Max(d, radial)) / 1.0, 0, 1);
                    var ga = glowAlpha * t * t;
                    (r, g, b, a) = Over(r, g, b, a, color.R / 255.0, color.G / 255.0, color.B / 255.0, ga);
                }

                // Body with Lambert shading and a specular highlight.
                var cover = Math.Clamp((1 - d) / px + 0.5, 0, 1);
                if (cover > 0)
                {
                    var nu = Math.Clamp(u / 1.0, -1, 1);
                    var nv = Math.Clamp(v / 1.0, -1, 1);
                    var n2 = Math.Min(0.98, nu * nu + nv * nv);
                    var nz = Math.Sqrt(1 - n2);
                    var nl = Math.Sqrt(nu * nu + nv * nv + nz * nz);
                    double nx = nu / nl, ny = nv / nl;
                    nz /= nl;
                    var lambert = Math.Max(0, nx * lx + ny * ly + nz * lz);
                    var spec = Math.Pow(Math.Max(0, nx * hx + ny * hy + nz * hz), 28) * (dark ? 0.55 : 0.45);
                    var shade = (dark ? 0.5 : 0.62) + (dark ? 0.62 : 0.48) * lambert;
                    var br = Math.Min(1, color.R / 255.0 * shade + spec);
                    var bg = Math.Min(1, color.G / 255.0 * shade + spec);
                    var bb = Math.Min(1, color.B / 255.0 * shade + spec);
                    (r, g, b, a) = Over(r, g, b, a, br, bg, bb, cover);
                }

                // Stale: a ring just outside the body.
                if (staleRing is { } s)
                {
                    var ring = Math.Clamp((0.13 - Math.Abs(d - 1.15)) / px + 0.5, 0, 1);
                    if (ring > 0) (r, g, b, a) = Over(r, g, b, a, s.R / 255.0, s.G / 255.0, s.B / 255.0, ring);
                }

                var o = (y * size + x) * 4;
                rgba[o] = (byte)Math.Round(r * 255);
                rgba[o + 1] = (byte)Math.Round(g * 255);
                rgba[o + 2] = (byte)Math.Round(b * 255);
                rgba[o + 3] = (byte)Math.Round(a * 255);
            }
        }
        return rgba;
    }

    /// <summary>
    /// A distance-like measure from the centre, 1 on the shape's edge: circle
    /// for memories, rounded square for chats, diamond for files.
    /// </summary>
    public static double ShapeDistance(int kind, double u, double v)
    {
        switch (kind)
        {
            case GraphLayout.Session:
            {
                // Rounded square of half-size 0.86 with corner radius 0.34.
                const double half = 0.86, rad = 0.34;
                var qx = Math.Abs(u) - (half - rad);
                var qy = Math.Abs(v) - (half - rad);
                var outside = Math.Sqrt(Math.Max(qx, 0) * Math.Max(qx, 0) + Math.Max(qy, 0) * Math.Max(qy, 0));
                var sd = outside + Math.Min(Math.Max(qx, qy), 0) - rad; // signed distance, 0 on the edge
                return 1 + sd / half;
            }
            case GraphLayout.File:
            {
                // Diamond: |u| + |v| = 1.05, edges slightly softened.
                return (Math.Abs(u) + Math.Abs(v)) / 1.05;
            }
            default:
                return Math.Sqrt(u * u + v * v);
        }
    }

    /// <summary>Straight-alpha "source over".</summary>
    private static (double R, double G, double B, double A) Over(double r, double g, double b, double a, double sr, double sg, double sb, double sa)
    {
        var oa = sa + a * (1 - sa);
        if (oa <= 0) return (0, 0, 0, 0);
        return ((sr * sa + r * a * (1 - sa)) / oa, (sg * sa + g * a * (1 - sa)) / oa, (sb * sa + b * a * (1 - sa)) / oa, oa);
    }
}

/// <summary>A minimal PNG encoder for 8-bit RGBA images.</summary>
internal static class Png
{
    private static readonly uint[] CrcTable = BuildCrcTable();

    public static byte[] Encode(int width, int height, byte[] rgba)
    {
        if (rgba.Length != width * height * 4) throw new ArgumentException("RGBA buffer size does not match the image.");
        using var output = new MemoryStream();
        output.Write([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A]);
        var header = new byte[13];
        WriteBigEndian(header, 0, (uint)width);
        WriteBigEndian(header, 4, (uint)height);
        header[8] = 8;  // bit depth
        header[9] = 6;  // colour type: RGBA
        WriteChunk(output, "IHDR", header);
        using (var raw = new MemoryStream())
        {
            using (var z = new ZLibStream(raw, CompressionLevel.Optimal, leaveOpen: true))
            {
                var row = width * 4;
                for (var y = 0; y < height; y++)
                {
                    z.WriteByte(0); // filter: none
                    z.Write(rgba, y * row, row);
                }
            }
            WriteChunk(output, "IDAT", raw.ToArray());
        }
        WriteChunk(output, "IEND", []);
        return output.ToArray();
    }

    private static void WriteChunk(Stream s, string type, byte[] data)
    {
        var len = new byte[4];
        WriteBigEndian(len, 0, (uint)data.Length);
        s.Write(len);
        var typeBytes = System.Text.Encoding.ASCII.GetBytes(type);
        s.Write(typeBytes);
        s.Write(data);
        var crc = 0xFFFFFFFFu;
        foreach (var b in typeBytes) crc = CrcTable[(crc ^ b) & 0xFF] ^ (crc >> 8);
        foreach (var b in data) crc = CrcTable[(crc ^ b) & 0xFF] ^ (crc >> 8);
        var c = new byte[4];
        WriteBigEndian(c, 0, crc ^ 0xFFFFFFFFu);
        s.Write(c);
    }

    private static void WriteBigEndian(byte[] buffer, int at, uint value)
    {
        buffer[at] = (byte)(value >> 24);
        buffer[at + 1] = (byte)(value >> 16);
        buffer[at + 2] = (byte)(value >> 8);
        buffer[at + 3] = (byte)value;
    }

    private static uint[] BuildCrcTable()
    {
        var table = new uint[256];
        for (uint n = 0; n < 256; n++)
        {
            var c = n;
            for (var k = 0; k < 8; k++) c = (c & 1) != 0 ? 0xEDB88320u ^ (c >> 1) : c >> 1;
            table[n] = c;
        }
        return table;
    }
}
