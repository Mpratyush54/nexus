#nullable enable
namespace Nexus.Core;

/// <summary>
/// A 3D force-directed layout for the memory graph (pure C#, no UI types, so
/// it is testable and runs off the UI thread). Velocity-Verlet in the style
/// of d3-force-3d: many-body repulsion, edge springs, a weak pull toward the
/// centre and a pull toward each linked cluster's centroid, all scaled by a
/// cooling "alpha". Positions are [x0, y0, z0, x1, ...]. Same input, same
/// result: seeds come from a hash of each node's id.
/// </summary>
internal sealed class ForceLayout3D
{
    public const double LinkDistance = 46;
    public const double AlphaMin = 0.001;
    /// <summary>Cools from 1 to AlphaMin in about 240 ticks.</summary>
    public static readonly double AlphaDecay = 1 - Math.Pow(AlphaMin, 1.0 / 240);
    private const double VelocityDecay = 0.42;
    private const double ChargeStrength = -70;
    private const double ChargeDistanceMax = 420;
    private const double CenterStrength = 0.012;
    private const double ClusterStrength = 0.035;

    public readonly int Count;
    public readonly double[] Positions;
    private readonly double[] _vel;
    private readonly int[] _edges;
    private readonly int[] _degree;
    private readonly double[] _weight;
    private readonly double[] _linkStrength;
    private readonly double[] _linkBias;
    private readonly int[] _comp;
    private readonly int _compCount;
    private readonly int[] _compSize;
    private readonly double[] _compCentroid;
    private readonly bool[] _pinned;

    public double Alpha { get; set; } = 1;
    public double AlphaTarget { get; set; }
    public int Ticks { get; private set; }

    /// <summary>True while the layout still moves (or is held warm by a drag).</summary>
    public bool Active => Alpha >= AlphaMin || AlphaTarget > 0;

    public ForceLayout3D(int n, int[] edges, string[] ids)
    {
        Count = n;
        _edges = edges;
        Positions = new double[n * 3];
        _vel = new double[n * 3];
        _pinned = new bool[n];
        _degree = GraphLayout.Degrees(n, edges);
        _weight = new double[n];
        for (var i = 0; i < n; i++) _weight[i] = 1 + Math.Sqrt(_degree[i]) * 0.35;
        var m = edges.Length / 2;
        _linkStrength = new double[m];
        _linkBias = new double[m];
        for (var e = 0; e < m; e++)
        {
            int a = edges[e * 2], b = edges[e * 2 + 1];
            _linkStrength[e] = 1.0 / Math.Max(1, Math.Min(_degree[a], _degree[b]));
            _linkBias[e] = _degree[a] / (double)Math.Max(1, _degree[a] + _degree[b]);
        }
        _comp = GraphLayout.Components(n, edges, out _compCount);
        _compSize = new int[_compCount];
        for (var i = 0; i < n; i++) _compSize[_comp[i]]++;
        _compCentroid = new double[_compCount * 3];
        Seed(ids);
    }

    /// <summary>
    /// Deterministic start: each linked cluster in a small ball around its
    /// own centre (the biggest at the origin, the rest on a sphere around it),
    /// so the layout visibly unfolds into place.
    /// </summary>
    private void Seed(string[] ids)
    {
        var shell = LinkDistance * (2.5 + Math.Sqrt(Count) * 0.35);
        var centres = new double[_compCount * 3];
        var k = 0;
        for (var c = 1; c < _compCount; c++)
        {
            var (x, y, z) = Fibonacci(k++, Math.Max(1, _compCount - 1));
            var r = _compSize[c] > 1 ? shell * 0.6 : shell;
            centres[c * 3] = x * r;
            centres[c * 3 + 1] = y * r;
            centres[c * 3 + 2] = z * r;
        }
        for (var i = 0; i < Count; i++)
        {
            var c = _comp[i];
            var h = GraphLayout.StableHash(ids.Length > i ? ids[i] : i.ToString());
            var h2 = h * 2654435761u + 97;
            var h3 = h2 * 2246822519u + 13;
            // A point in the unit ball from three hashes.
            var u = (h & 0xFFFF) / 65535.0 * 2 - 1;
            var theta = ((h >> 16) & 0xFFFF) / 65535.0 * Math.PI * 2;
            var rr = Math.Cbrt(((h3 >> 8) & 0xFFFF) / 65535.0 * 0.9 + 0.1);
            var s = Math.Sqrt(1 - u * u);
            var spread = LinkDistance * 0.55 * Math.Cbrt(_compSize[c]);
            Positions[i * 3] = centres[c * 3] + s * Math.Cos(theta) * rr * spread;
            Positions[i * 3 + 1] = centres[c * 3 + 1] + u * rr * spread;
            Positions[i * 3 + 2] = centres[c * 3 + 2] + s * Math.Sin(theta) * rr * spread + ((h2 >> 20) & 7) * 0.01;
        }
    }

    /// <summary>The k-th of n points spread evenly on the unit sphere.</summary>
    public static (double X, double Y, double Z) Fibonacci(int k, int n)
    {
        var y = n <= 1 ? 0 : 1 - 2 * (k + 0.5) / n;
        var r = Math.Sqrt(Math.Max(0, 1 - y * y));
        var phi = k * Math.PI * (3 - Math.Sqrt(5));
        return (Math.Cos(phi) * r, y, Math.Sin(phi) * r);
    }

    public bool IsPinned(int i) => _pinned[i];

    /// <summary>Holds node i at a fixed spot (while it is dragged).</summary>
    public void Pin(int i, double x, double y, double z)
    {
        if (i < 0 || i >= Count) return;
        _pinned[i] = true;
        Positions[i * 3] = x;
        Positions[i * 3 + 1] = y;
        Positions[i * 3 + 2] = z;
        _vel[i * 3] = _vel[i * 3 + 1] = _vel[i * 3 + 2] = 0;
    }

    public void Unpin(int i)
    {
        if (i >= 0 && i < Count) _pinned[i] = false;
    }

    /// <summary>Warms the layout back up (after a drag or a reload).</summary>
    public void Reheat(double alpha) => Alpha = Math.Max(Alpha, alpha);

    /// <summary>Runs ticks until cool (or max ticks); returns ticks run.</summary>
    public int RunToRest(int maxTicks = 1000, CancellationToken ct = default)
    {
        var t = 0;
        while (Active && t < maxTicks)
        {
            if ((t & 15) == 0) ct.ThrowIfCancellationRequested();
            Tick();
            t++;
        }
        return t;
    }

    public void Tick()
    {
        Alpha += (AlphaTarget - Alpha) * AlphaDecay;
        Ticks++;
        var n = Count;
        var p = Positions;
        var v = _vel;
        var alpha = Alpha;

        // Edge springs (d3 link force: bias by degree, strength 1/min degree).
        for (var e = 0; e < _linkStrength.Length; e++)
        {
            int a = _edges[e * 2], b = _edges[e * 2 + 1];
            if (a == b) continue;
            var dx = p[b * 3] + v[b * 3] - p[a * 3] - v[a * 3];
            var dy = p[b * 3 + 1] + v[b * 3 + 1] - p[a * 3 + 1] - v[a * 3 + 1];
            var dz = p[b * 3 + 2] + v[b * 3 + 2] - p[a * 3 + 2] - v[a * 3 + 2];
            var l = Math.Sqrt(dx * dx + dy * dy + dz * dz);
            if (l < 1e-6) { dx = 0.01 * ((e % 3) - 1) + 0.005; l = Math.Abs(dx); }
            var f = (l - LinkDistance) / l * alpha * _linkStrength[e];
            dx *= f; dy *= f; dz *= f;
            var bias = _linkBias[e];
            v[b * 3] -= dx * bias; v[b * 3 + 1] -= dy * bias; v[b * 3 + 2] -= dz * bias;
            v[a * 3] += dx * (1 - bias); v[a * 3 + 1] += dy * (1 - bias); v[a * 3 + 2] += dz * (1 - bias);
        }

        // Many-body repulsion, all pairs within reach (n ≤ 1000 keeps this cheap).
        var maxD2 = ChargeDistanceMax * ChargeDistanceMax;
        for (var i = 0; i < n; i++)
        {
            double xi = p[i * 3], yi = p[i * 3 + 1], zi = p[i * 3 + 2], wi = _weight[i];
            double ax = 0, ay = 0, az = 0;
            for (var j = i + 1; j < n; j++)
            {
                var dx = p[j * 3] - xi;
                var dy = p[j * 3 + 1] - yi;
                var dz = p[j * 3 + 2] - zi;
                var d2 = dx * dx + dy * dy + dz * dz;
                if (d2 >= maxD2) continue;
                if (d2 < 1)
                {
                    if (d2 < 1e-9)
                    {
                        dx = 0.01 * (((i * 7 + j) % 5) - 2) + 0.003;
                        dy = 0.01 * (((i + j * 3) % 5) - 2) + 0.002;
                        dz = 0.01 * (((i * 5 + j * 11) % 5) - 2) + 0.001;
                    }
                    d2 = 1;
                }
                var w = ChargeStrength * alpha / d2;
                var wj = w * _weight[j];
                var wiw = w * wi;
                // Each node is pushed in proportion to the other's weight.
                ax += dx * wj; ay += dy * wj; az += dz * wj;
                v[j * 3] -= dx * wiw; v[j * 3 + 1] -= dy * wiw; v[j * 3 + 2] -= dz * wiw;
            }
            v[i * 3] += ax; v[i * 3 + 1] += ay; v[i * 3 + 2] += az;
        }

        // Cluster cohesion: pull linked nodes toward their cluster's centroid.
        Array.Clear(_compCentroid);
        for (var i = 0; i < n; i++)
        {
            var c = _comp[i];
            _compCentroid[c * 3] += p[i * 3];
            _compCentroid[c * 3 + 1] += p[i * 3 + 1];
            _compCentroid[c * 3 + 2] += p[i * 3 + 2];
        }
        for (var c = 0; c < _compCount; c++)
        {
            var s = Math.Max(1, _compSize[c]);
            _compCentroid[c * 3] /= s; _compCentroid[c * 3 + 1] /= s; _compCentroid[c * 3 + 2] /= s;
        }
        for (var i = 0; i < n; i++)
        {
            var c = _comp[i];
            var k = alpha * CenterStrength;
            v[i * 3] -= p[i * 3] * k; v[i * 3 + 1] -= p[i * 3 + 1] * k; v[i * 3 + 2] -= p[i * 3 + 2] * k;
            if (_compSize[c] <= 1) continue;
            var kc = alpha * ClusterStrength;
            v[i * 3] += (_compCentroid[c * 3] - p[i * 3]) * kc;
            v[i * 3 + 1] += (_compCentroid[c * 3 + 1] - p[i * 3 + 1]) * kc;
            v[i * 3 + 2] += (_compCentroid[c * 3 + 2] - p[i * 3 + 2]) * kc;
        }

        // Integrate.
        for (var i = 0; i < n; i++)
        {
            if (_pinned[i])
            {
                v[i * 3] = v[i * 3 + 1] = v[i * 3 + 2] = 0;
                continue;
            }
            for (var d = 0; d < 3; d++)
            {
                var vi = v[i * 3 + d] * (1 - VelocityDecay);
                v[i * 3 + d] = vi;
                p[i * 3 + d] += vi;
            }
        }
    }
}

/// <summary>A screen projection of a world point.</summary>
internal readonly record struct Projected(double X, double Y, double Depth, double Scale, bool Visible);

/// <summary>
/// An orbit camera: it looks at Target from Distance, turned by Yaw (around
/// the vertical axis) and Pitch (up/down). Perspective projection with a
/// vertical field of view; screen y grows downwards.
/// </summary>
internal sealed class Camera3D
{
    public const double Near = 1;
    public const double MinDistance = 20;
    public const double MaxDistance = 20000;
    public const double MaxPitch = 1.45;

    public double Yaw, Pitch, Distance = 600;
    public double TargetX, TargetY, TargetZ;
    public double FovY = 50 * Math.PI / 180;

    public Camera3D Clone() => (Camera3D)MemberwiseClone();

    public void CopyFrom(Camera3D src)
    {
        Yaw = src.Yaw;
        Pitch = src.Pitch;
        Distance = src.Distance;
        TargetX = src.TargetX;
        TargetY = src.TargetY;
        TargetZ = src.TargetZ;
        FovY = src.FovY;
    }

    public double Focal(double height) => height / 2 / Math.Tan(FovY / 2);

    /// <summary>Forward (view direction), right and up unit vectors.</summary>
    public void Basis(out (double X, double Y, double Z) f, out (double X, double Y, double Z) r, out (double X, double Y, double Z) u)
    {
        double cy = Math.Cos(Yaw), sy = Math.Sin(Yaw), cp = Math.Cos(Pitch), sp = Math.Sin(Pitch);
        f = (-cp * sy, -sp, -cp * cy);
        r = (cy, 0, -sy);
        // u = r × f
        u = (r.Y * f.Z - r.Z * f.Y, r.Z * f.X - r.X * f.Z, r.X * f.Y - r.Y * f.X);
    }

    public (double X, double Y, double Z) Position
    {
        get
        {
            Basis(out var f, out _, out _);
            return (TargetX - f.X * Distance, TargetY - f.Y * Distance, TargetZ - f.Z * Distance);
        }
    }

    /// <summary>A cached view for projecting many points per frame.</summary>
    public readonly struct View
    {
        public readonly double Cx, Cy, Cz, Fx, Fy, Fz, Rx, Ry, Rz, Ux, Uy, Uz, Focal, HalfW, HalfH;

        public View(Camera3D cam, double width, double height)
        {
            cam.Basis(out var f, out var r, out var u);
            var c = cam.Position;
            (Cx, Cy, Cz) = c;
            (Fx, Fy, Fz) = f;
            (Rx, Ry, Rz) = r;
            (Ux, Uy, Uz) = u;
            Focal = cam.Focal(height);
            HalfW = width / 2;
            HalfH = height / 2;
        }

        public Projected Project(double x, double y, double z)
        {
            var vx = x - Cx;
            var vy = y - Cy;
            var vz = z - Cz;
            var depth = vx * Fx + vy * Fy + vz * Fz;
            if (depth < Near) return new Projected(0, 0, depth, 0, false);
            var s = Focal / depth;
            var xc = vx * Rx + vy * Ry + vz * Rz;
            var yc = vx * Ux + vy * Uy + vz * Uz;
            return new Projected(HalfW + xc * s, HalfH - yc * s, depth, s, true);
        }
    }

    public Projected Project(double x, double y, double z, double width, double height) =>
        new View(this, width, height).Project(x, y, z);

    public void Orbit(double dYaw, double dPitch)
    {
        Yaw = NormalizeAngle(Yaw + dYaw);
        Pitch = Math.Clamp(Pitch + dPitch, -MaxPitch, MaxPitch);
    }

    /// <summary>The world move that shifts a point at the given depth by (dx, dy) pixels on screen.</summary>
    public (double X, double Y, double Z) ScreenDeltaToWorld(double dx, double dy, double depth, double height)
    {
        Basis(out _, out var r, out var u);
        var k = Math.Max(Near, depth) / Focal(height);
        return (r.X * dx * k - u.X * dy * k, r.Y * dx * k - u.Y * dy * k, r.Z * dx * k - u.Z * dy * k);
    }

    /// <summary>Pans so that what is at the target depth follows the pointer by (dx, dy) pixels.</summary>
    public void Pan(double dx, double dy, double height)
    {
        var (x, y, z) = ScreenDeltaToWorld(dx, dy, Distance, height);
        TargetX -= x;
        TargetY -= y;
        TargetZ -= z;
    }

    /// <summary>
    /// Zooms by factor (&gt;1 = closer) keeping the point under (sx, sy), on
    /// the plane through the target, fixed on screen.
    /// </summary>
    public void ZoomAt(double sx, double sy, double factor, double width, double height)
    {
        if (factor <= 0) return;
        var newD = Math.Clamp(Distance / factor, MinDistance, MaxDistance);
        var (ox, oy, oz) = ScreenDeltaToWorld(sx - width / 2, sy - height / 2, Distance, height);
        var qx = TargetX + ox;
        var qy = TargetY + oy;
        var qz = TargetZ + oz;
        var t = newD / Distance;
        TargetX = qx + (TargetX - qx) * t;
        TargetY = qy + (TargetY - qy) * t;
        TargetZ = qz + (TargetZ - qz) * t;
        Distance = newD;
    }

    /// <summary>Distance at which a sphere of radius r fits the viewport.</summary>
    public double FitDistance(double radius, double width, double height, double margin = 1.12)
    {
        var halfV = FovY / 2;
        var halfH = Math.Atan(Math.Tan(halfV) * Math.Max(0.1, width / Math.Max(1, height)));
        var half = Math.Min(halfV, halfH);
        return Math.Clamp(Math.Max(1, radius) * margin / Math.Sin(half), MinDistance, MaxDistance);
    }

    /// <summary>Camera between a and b at t (angles take the short way round).</summary>
    public static Camera3D Lerp(Camera3D a, Camera3D b, double t)
    {
        var c = a.Clone();
        LerpInto(a, b, t, c);
        return c;
    }

    /// <summary>Lerp into an existing camera (no allocation, for the frame loop).</summary>
    public static void LerpInto(Camera3D a, Camera3D b, double t, Camera3D c)
    {
        c.FovY = a.FovY;
        c.Yaw = NormalizeAngle(a.Yaw + NormalizeAngle(b.Yaw - a.Yaw) * t);
        c.Pitch = a.Pitch + (b.Pitch - a.Pitch) * t;
        // Distance in log space feels even when zooming far.
        c.Distance = Math.Exp(Math.Log(a.Distance) + (Math.Log(b.Distance) - Math.Log(a.Distance)) * t);
        c.TargetX = a.TargetX + (b.TargetX - a.TargetX) * t;
        c.TargetY = a.TargetY + (b.TargetY - a.TargetY) * t;
        c.TargetZ = a.TargetZ + (b.TargetZ - a.TargetZ) * t;
    }

    /// <summary>
    /// Pans so the target lands at (sx, sy) on screen instead of the centre
    /// (to keep a selection in the part of the view not under a panel).
    /// </summary>
    public void PlaceTargetAt(double sx, double sy, double width, double height) =>
        Pan(sx - width / 2, sy - height / 2, height);

    public static double EaseInOut(double t)
    {
        t = Math.Clamp(t, 0, 1);
        return t < 0.5 ? 4 * t * t * t : 1 - Math.Pow(-2 * t + 2, 3) / 2;
    }

    public static double NormalizeAngle(double a)
    {
        a %= 2 * Math.PI;
        if (a > Math.PI) a -= 2 * Math.PI;
        if (a < -Math.PI) a += 2 * Math.PI;
        return a;
    }
}

/// <summary>Per-frame helpers for drawing and picking a projected graph.</summary>
internal static class Graph3D
{
    /// <summary>
    /// The node under (x, y): of all shown nodes whose on-screen disc (plus
    /// slack pixels) contains the point, the one nearest the camera; −1 if none.
    /// </summary>
    public static int HitTest(double[] sx, double[] sy, double[] radius, double[] depth, bool[] shown, double x, double y, double slack = 3)
    {
        var best = -1;
        var bestDepth = double.MaxValue;
        for (var i = 0; i < sx.Length; i++)
        {
            if (!shown[i]) continue;
            var r = radius[i] + slack;
            var dx = sx[i] - x;
            var dy = sy[i] - y;
            if (dx * dx + dy * dy > r * r) continue;
            if (depth[i] < bestDepth)
            {
                bestDepth = depth[i];
                best = i;
            }
        }
        return best;
    }

    /// <summary>
    /// Shown node indexes, farthest first (painter's order), into order;
    /// returns how many. keys is scratch space of the same length.
    /// </summary>
    public static int DepthOrder(double[] depth, bool[] shown, int[] order, double[] keys)
    {
        var count = 0;
        for (var i = 0; i < depth.Length; i++)
        {
            if (!shown[i]) continue;
            order[count] = i;
            keys[count] = -depth[i];
            count++;
        }
        Array.Sort(keys, order, 0, count);
        return count;
    }

    /// <summary>
    /// Depth cue in [0, 1]: 1 at the nearest depth, falling to 0 at the
    /// farthest. Drives size, brightness and fog.
    /// </summary>
    public static double DepthCue(double depth, double near, double far)
    {
        if (far - near < 1e-6) return 1;
        return 1 - Math.Clamp((depth - near) / (far - near), 0, 1);
    }

    /// <summary>Centre and radius of a sphere around the shown points (centroid based).</summary>
    public static (double X, double Y, double Z, double R) Bounds(double[] pos, bool[] shown)
    {
        double sx = 0, sy = 0, sz = 0;
        var count = 0;
        for (var i = 0; i < shown.Length; i++)
        {
            if (!shown[i]) continue;
            sx += pos[i * 3]; sy += pos[i * 3 + 1]; sz += pos[i * 3 + 2];
            count++;
        }
        if (count == 0) return (0, 0, 0, 1);
        sx /= count; sy /= count; sz /= count;
        double r2 = 0;
        for (var i = 0; i < shown.Length; i++)
        {
            if (!shown[i]) continue;
            var dx = pos[i * 3] - sx;
            var dy = pos[i * 3 + 1] - sy;
            var dz = pos[i * 3 + 2] - sz;
            r2 = Math.Max(r2, dx * dx + dy * dy + dz * dz);
        }
        return (sx, sy, sz, Math.Max(20, Math.Sqrt(r2)));
    }

    /// <summary>
    /// Where a node's sprite goes: the image's body radius is a quarter of
    /// its size, so a body of radius r px centred on (sx, sy) needs a 4r
    /// square whose top-left corner is (sx − 2r, sy − 2r).
    /// </summary>
    public static (double Left, double Top, double Size) SpriteRect(double sx, double sy, double radiusPx)
    {
        var size = radiusPx / NodeSprite.BodyFraction;
        return (sx - size / 2, sy - size / 2, size);
    }

    /// <summary>
    /// An edge as a thin quad: its top-left corner (before rotation), length
    /// and angle (radians), rotating around (0, thickness / 2) so the quad's
    /// centre line runs from (x1, y1) to (x2, y2).
    /// </summary>
    public static (double Left, double Top, double Length, double Angle) EdgeQuad(double x1, double y1, double x2, double y2, double thickness)
    {
        var dx = x2 - x1;
        var dy = y2 - y1;
        return (x1, y1 - thickness / 2, Math.Sqrt(dx * dx + dy * dy), Math.Atan2(dy, dx));
    }

    /// <summary>
    /// Frames the shown nodes in the visible part of the viewport (the area
    /// left of a side panel, above a legend): start from a bounding-sphere
    /// fit, then a few rounds of measuring the projected on-screen box,
    /// including node radii and label extents (labelRight[i] px to the right
    /// of node i), and panning/zooming it into the area with padding.
    /// </summary>
    public static void FitToScreen(Camera3D cam, double[] pos, bool[] shown, double[] worldRadius, double[] labelRight,
        double width, double height, double insetLeft, double insetTop, double insetRight, double insetBottom, double padding = 24)
    {
        var any = false;
        for (var i = 0; i < shown.Length && !any; i++) any = shown[i];
        if (!any || width < 2 || height < 2) return;
        var (areaL, areaT, areaR, areaB) = FitArea(width, height, insetLeft, insetTop, insetRight, insetBottom, padding);
        var areaW = areaR - areaL;
        var areaH = areaB - areaT;
        // Labels are fixed-size pixels: if they could claim the whole area the
        // fit would shrink the graph to a speck. Reserve at most a share for them
        // (labels near the edge flip to the node's other side when drawn).
        var labelCap = areaW * 0.22;
        var (cx, cy, cz, r) = Bounds(pos, shown);
        cam.TargetX = cx;
        cam.TargetY = cy;
        cam.TargetZ = cz;
        cam.Distance = cam.FitDistance(r, width, height, 1.0);
        for (var round = 0; round < 8; round++)
        {
            var b = ScreenBox(cam, pos, shown, worldRadius, labelRight, width, height, labelCap, OutlierTrim);
            if (b is not { } box) return;
            var bw = Math.Max(1, box.Right - box.Left);
            var bh = Math.Max(1, box.Bottom - box.Top);
            var factor = Math.Clamp(Math.Min(areaW / bw, areaH / bh), 0.25, 4);
            // Centre the box in the area, then zoom about the area's centre.
            var dx = (areaL + areaR) / 2 - (box.Left + box.Right) / 2;
            var dy = (areaT + areaB) / 2 - (box.Top + box.Bottom) / 2;
            cam.Pan(dx, dy, height);
            cam.ZoomAt((areaL + areaR) / 2, (areaT + areaB) / 2, factor, width, height);
            if (Math.Abs(factor - 1) < 0.01 && Math.Abs(dx) < 0.5 && Math.Abs(dy) < 0.5) break;
        }
        // Never end with part of the box outside the area: a last zoom-out if needed.
        if (ScreenBox(cam, pos, shown, worldRadius, labelRight, width, height, labelCap, OutlierTrim) is { } last)
        {
            var bw = Math.Max(1, last.Right - last.Left);
            var bh = Math.Max(1, last.Bottom - last.Top);
            var over = Math.Max(bw / areaW, bh / areaH);
            if (over > 1) cam.ZoomAt((areaL + areaR) / 2, (areaT + areaB) / 2, 1 / (over * 1.02), width, height);
        }
    }

    /// <summary>
    /// A label's left edge: right of the node when it fits before maxX, else
    /// left of the node when that fits after minX, else pushed inside
    /// [minX, maxX] on whichever side has more room. Never clipped by the edge
    /// or a panel unless the label is wider than the whole range.
    /// </summary>
    public static double LabelX(double sx, double radius, double width, double minX, double maxX)
    {
        const double gap = 5;
        var right = sx + radius + gap;
        if (right + width <= maxX) return right;
        var left = sx - radius - gap - width;
        if (left >= minX) return left;
        var x = maxX - right > left + width - minX ? right : left;
        return Math.Max(minX, Math.Min(x, maxX - width));
    }

    /// <summary>Share of nodes on each side the fit may leave out (far outliers).</summary>
    public const double OutlierTrim = 0.025;

    /// <summary>
    /// The part of the viewport a fit fills: inside the insets and padding,
    /// but never narrower than 35% of the view (or 120 px): an inset that
    /// would leave less is ignored rather than collapsing the fit.
    /// </summary>
    public static (double Left, double Top, double Right, double Bottom) FitArea(double width, double height,
        double insetLeft, double insetTop, double insetRight, double insetBottom, double padding)
    {
        var pad = Math.Min(padding, Math.Min(width, height) / 6);
        double l = Math.Max(0, insetLeft) + pad, r = width - Math.Max(0, insetRight) - pad;
        double t = Math.Max(0, insetTop) + pad, b = height - Math.Max(0, insetBottom) - pad;
        if (r - l < Math.Max(120, width * 0.35)) { l = pad; r = width - pad; }
        if (b - t < Math.Max(90, height * 0.35)) { t = pad; b = height - pad; }
        if (r - l < 1) { l = 0; r = Math.Max(1, width); }
        if (b - t < 1) { t = 0; b = Math.Max(1, height); }
        return (l, t, r, b);
    }

    /// <summary>
    /// The on-screen box around shown nodes (radius and label, capped at
    /// labelCap px, included); null if none is in front. With trim &gt; 0 and
    /// enough nodes, that share of the extremes on each side is ignored, so a
    /// few far outliers don't shrink everything else.
    /// </summary>
    public static (double Left, double Top, double Right, double Bottom)? ScreenBox(Camera3D cam, double[] pos, bool[] shown, double[] worldRadius, double[] labelRight,
        double width, double height, double labelCap = double.MaxValue, double trim = 0)
    {
        var view = new Camera3D.View(cam, width, height);
        var count = 0;
        for (var i = 0; i < shown.Length; i++) if (shown[i]) count++;
        if (count == 0) return null;
        var ls = new double[count];
        var rs = new double[count];
        var ts = new double[count];
        var bs = new double[count];
        var k = 0;
        for (var i = 0; i < shown.Length; i++)
        {
            if (!shown[i]) continue;
            var p = view.Project(pos[i * 3], pos[i * 3 + 1], pos[i * 3 + 2]);
            if (!p.Visible) continue;
            var rad = worldRadius[i] * p.Scale;
            ls[k] = p.X - rad;
            rs[k] = p.X + rad + Math.Min(labelCap, labelRight.Length > i ? labelRight[i] : 0);
            ts[k] = p.Y - Math.Max(rad, 10);
            bs[k] = p.Y + Math.Max(rad, 10);
            k++;
        }
        if (k == 0) return null;
        var cut = k >= 30 ? (int)Math.Floor(k * Math.Clamp(trim, 0, 0.2)) : 0;
        Array.Sort(ls, 0, k);
        Array.Sort(rs, 0, k);
        Array.Sort(ts, 0, k);
        Array.Sort(bs, 0, k);
        return (ls[cut], ts[cut], rs[k - 1 - cut], bs[k - 1 - cut]);
    }

    /// <summary>Node radius in world units for the 3D view.</summary>
    public static double NodeRadius(int kind, int degree) => GraphLayout.NodeRadius(kind, degree) * 0.55;
}
