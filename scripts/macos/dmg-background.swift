// Renders the Nexus DMG window background: the installer's ink, cream and
// gold (desktop/installer), the mark, a "drag to Applications" trail drawn as
// a small memory graph, and the unsigned-beta Gatekeeper note.
//
//   swift scripts/macos/dmg-background.swift <Nexus.png> <out-dir>
//
// Writes background.png (660x400) and background@2x.png; package-macos.sh
// joins them into one HiDPI background.tiff. The positions here must match
// scripts/macos/dmg-settings.py (window size, icon centres, icon size).
import AppKit

let W: CGFloat = 660, H: CGFloat = 400
let appCentre = CGPoint(x: 170, y: 196)
let appsCentre = CGPoint(x: 490, y: 196)
let iconSize: CGFloat = 112

func rgb(_ hex: UInt32, _ a: CGFloat = 1) -> NSColor {
    NSColor(srgbRed: CGFloat((hex >> 16) & 0xFF) / 255, green: CGFloat((hex >> 8) & 0xFF) / 255, blue: CGFloat(hex & 0xFF) / 255, alpha: a)
}
let ink = rgb(0x0E0F0E), cream = rgb(0xF2EFE6), gold = rgb(0xE8BB67), muted = rgb(0x9A978E)
let faint = rgb(0x6E6C66), indigo = rgb(0x8EA2FF)
// Finder draws icon labels black in Light mode and white in Dark mode; a
// bronze plate (relative luminance ~0.15) keeps both above 4:1.
let plate = rgb(0x806A42)

let args = CommandLine.arguments
guard args.count >= 3, let mark = NSImage(contentsOfFile: args[1]) else {
    FileHandle.standardError.write("usage: dmg-background.swift <Nexus.png> <out-dir>\n".data(using: .utf8)!)
    exit(2)
}
let outDir = args[2]

func glow(_ ctx: CGContext, _ c: CGPoint, _ r: CGFloat, _ color: NSColor, _ alpha: CGFloat) {
    let colors = [color.withAlphaComponent(alpha).cgColor, color.withAlphaComponent(0).cgColor] as CFArray
    let g = CGGradient(colorsSpace: CGColorSpace(name: CGColorSpace.sRGB), colors: colors, locations: [0, 1])!
    ctx.drawRadialGradient(g, startCenter: c, startRadius: 0, endCenter: c, endRadius: r, options: [])
}

func dot(_ ctx: CGContext, _ c: CGPoint, _ d: CGFloat, _ color: NSColor) {
    ctx.setFillColor(color.cgColor)
    ctx.fillEllipse(in: CGRect(x: c.x - d / 2, y: c.y - d / 2, width: d, height: d))
}

@discardableResult
func text(_ s: String, _ font: NSFont, _ color: NSColor, centreX: CGFloat, top: CGFloat, kern: CGFloat = 0) -> CGSize {
    let style = NSMutableParagraphStyle()
    style.alignment = .center
    let attr = NSAttributedString(string: s, attributes: [.font: font, .foregroundColor: color, .kern: kern, .paragraphStyle: style])
    let size = attr.size()
    attr.draw(at: CGPoint(x: centreX - size.width / 2, y: top))
    return size
}

func render(scale s: CGFloat, to path: String) {
    let space = CGColorSpace(name: CGColorSpace.sRGB)!
    let ctx = CGContext(data: nil, width: Int(W * s), height: Int(H * s), bitsPerComponent: 8, bytesPerRow: 0,
                        space: space, bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
    // Top-left origin, points, like Finder's icon positions.
    ctx.scaleBy(x: s, y: s)
    ctx.translateBy(x: 0, y: H)
    ctx.scaleBy(x: 1, y: -1)
    NSGraphicsContext.current = NSGraphicsContext(cgContext: ctx, flipped: true)
    ctx.setShouldAntialias(true)
    ctx.interpolationQuality = .high

    // Ink, with the installer's ambient light: warm top-left, cool bottom-right.
    ctx.setFillColor(ink.cgColor)
    ctx.fill(CGRect(x: 0, y: 0, width: W, height: H))
    glow(ctx, CGPoint(x: 30, y: -20), 380, gold, 0.17)
    glow(ctx, CGPoint(x: 690, y: 440), 400, indigo, 0.12)

    // Header: the mark and the name, the tagline below.
    let markSize: CGFloat = 30
    let title = NSAttributedString(string: "Nexus", attributes: [.font: NSFont.systemFont(ofSize: 22, weight: .semibold)])
    let titleW = title.size().width
    let rowW = markSize + 10 + titleW
    let rowX = W / 2 - rowW / 2
    mark.draw(in: CGRect(x: rowX, y: 30, width: markSize, height: markSize), from: .zero, operation: .sourceOver, fraction: 1,
              respectFlipped: true, hints: [.interpolation: NSImageInterpolation.high.rawValue])
    text("Nexus", NSFont.systemFont(ofSize: 22, weight: .semibold), cream, centreX: rowX + markSize + 10 + titleW / 2, top: 31)
    text("Shared memory for your AI agents", NSFont.systemFont(ofSize: 13), muted, centreX: W / 2, top: 66)

    // The orbit ring around Nexus.app, as on the installer's welcome screen.
    let ringR: CGFloat = iconSize / 2 + 16
    ctx.setStrokeColor(cream.withAlphaComponent(0.10).cgColor)
    ctx.setLineWidth(1)
    ctx.strokeEllipse(in: CGRect(x: appCentre.x - ringR, y: appCentre.y - ringR, width: ringR * 2, height: ringR * 2))
    glow(ctx, appCentre, ringR + 10, gold, 0.16)
    for i in 0..<7 {
        let a = Double(i) * .pi * 2 / 7 + 0.5
        let p = CGPoint(x: appCentre.x + CGFloat(cos(a)) * ringR, y: appCentre.y + CGFloat(sin(a)) * ringR)
        // Skip the dots the label plate would cover.
        if p.y > appCentre.y + ringR * 0.75 { continue }
        dot(ctx, p, i % 3 == 0 ? 6.5 : 4.5, i % 3 == 0 ? gold : cream.withAlphaComponent(0.75))
    }
    // A quiet target ring around Applications.
    ctx.setStrokeColor(gold.withAlphaComponent(0.28).cgColor)
    ctx.setLineDash(phase: 0, lengths: [3, 5])
    ctx.strokeEllipse(in: CGRect(x: appsCentre.x - ringR, y: appsCentre.y - ringR, width: ringR * 2, height: ringR * 2))
    ctx.setLineDash(phase: 0, lengths: [])

    // The drag trail: a memory graph forming from Nexus towards Applications.
    let start = CGPoint(x: appCentre.x + ringR + 10, y: appCentre.y)
    let end = CGPoint(x: appsCentre.x - ringR - 14, y: appCentre.y)
    let control = CGPoint(x: (start.x + end.x) / 2, y: appCentre.y - 30)
    func bez(_ t: CGFloat) -> CGPoint {
        let u = 1 - t
        return CGPoint(x: u * u * start.x + 2 * u * t * control.x + t * t * end.x,
                       y: u * u * start.y + 2 * u * t * control.y + t * t * end.y)
    }
    let n = 7
    let pts = (0...n).map { bez(CGFloat($0) / CGFloat(n)) }
    ctx.setStrokeColor(cream.withAlphaComponent(0.30).cgColor)
    ctx.setLineWidth(1.2)
    ctx.move(to: pts[0])
    for p in pts.dropFirst() { ctx.addLine(to: p) }
    ctx.strokePath()
    for (i, p) in pts.enumerated() where i < n {
        let size: CGFloat = i % 3 == 0 ? 7.5 : 5
        let color = i % 3 == 0 ? gold : i % 2 == 0 ? indigo : cream.withAlphaComponent(0.85)
        dot(ctx, p, size, color)
    }
    // Arrowhead along the last segment.
    let tip = pts[n]
    let prev = bez(0.92)
    let ang = atan2(tip.y - prev.y, tip.x - prev.x)
    let len: CGFloat = 12
    ctx.setStrokeColor(gold.cgColor)
    ctx.setLineWidth(2.4)
    ctx.setLineCap(.round)
    ctx.setLineJoin(.round)
    ctx.move(to: CGPoint(x: tip.x - len * cos(ang - 0.55), y: tip.y - len * sin(ang - 0.55)))
    ctx.addLine(to: tip)
    ctx.addLine(to: CGPoint(x: tip.x - len * cos(ang + 0.55), y: tip.y - len * sin(ang + 0.55)))
    ctx.strokePath()
    text("Drag Nexus to Applications", NSFont.systemFont(ofSize: 13, weight: .semibold), cream,
         centreX: W / 2, top: appCentre.y + 18)

    // Plates under Finder's icon labels (see `plate`).
    for c in [appCentre, appsCentre] {
        let r = CGRect(x: c.x - 62, y: c.y + iconSize / 2 + 5, width: 124, height: 22)
        ctx.setFillColor(plate.withAlphaComponent(0.92).cgColor)
        ctx.addPath(CGPath(roundedRect: r, cornerWidth: 11, cornerHeight: 11, transform: nil))
        ctx.fillPath()
    }

    // Unsigned beta: what to do when macOS won't open it the first time.
    ctx.setFillColor(cream.withAlphaComponent(0.07).cgColor)
    ctx.fill(CGRect(x: 40, y: 330, width: W - 80, height: 1))
    text("If macOS blocks the first open (unsigned beta):", NSFont.systemFont(ofSize: 11.5, weight: .medium),
         cream.withAlphaComponent(0.78), centreX: W / 2, top: 343)
    text("right-click Nexus → Open,  or  System Settings → Privacy & Security → Open Anyway",
         NSFont.systemFont(ofSize: 11), muted, centreX: W / 2, top: 362)

    NSGraphicsContext.current = nil
    let image = ctx.makeImage()!
    let rep = NSBitmapImageRep(cgImage: image)
    rep.size = NSSize(width: W, height: H) // 72 or 144 dpi, so Finder picks the right one
    try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: path))
    print("wrote \(path) (\(Int(W * s))x\(Int(H * s)))")
}

render(scale: 1, to: outDir + "/background.png")
render(scale: 2, to: outDir + "/background@2x.png")
