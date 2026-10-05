#!/usr/bin/env swift
// Render the shared vector mark into the sizes required by a native macOS icon.
import AppKit
import Foundation

struct IconError: LocalizedError {
    let message: String
    var errorDescription: String? { message }
}

func generateIcon(source: String, destination: String) throws {
    guard let image = NSImage(contentsOfFile: source) else {
        throw IconError(message: "Cannot read workbench icon: \(source)")
    }
    let files = FileManager.default
    let temporary = files.temporaryDirectory.appendingPathComponent("workbench-icon-\(UUID().uuidString)", isDirectory: true)
    let iconset = temporary.appendingPathComponent("workbench.iconset", isDirectory: true)
    try files.createDirectory(at: iconset, withIntermediateDirectories: true)
    defer { try? files.removeItem(at: temporary) }

    for points in [16, 32, 128, 256, 512] {
        for scale in [1, 2] {
            let pixels = points * scale
            guard let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: pixels, pixelsHigh: pixels, bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0),
                  let context = NSGraphicsContext(bitmapImageRep: bitmap) else {
                throw IconError(message: "Cannot allocate workbench icon bitmap")
            }
            NSGraphicsContext.saveGraphicsState()
            NSGraphicsContext.current = context
            context.imageInterpolation = .high
            image.draw(in: NSRect(x: 0, y: 0, width: pixels, height: pixels), from: .zero, operation: .copy, fraction: 1)
            context.flushGraphics()
            NSGraphicsContext.restoreGraphicsState()
            guard let png = bitmap.representation(using: .png, properties: [:]) else {
                throw IconError(message: "Cannot encode workbench icon bitmap")
            }
            let suffix = scale == 2 ? "@2x" : ""
            try png.write(to: iconset.appendingPathComponent("icon_\(points)x\(points)\(suffix).png"))
        }
    }
    let output = URL(fileURLWithPath: destination)
    try files.createDirectory(at: output.deletingLastPathComponent(), withIntermediateDirectories: true)
    let process = Process()
    process.executableURL = URL(fileURLWithPath: "/usr/bin/iconutil")
    process.arguments = ["-c", "icns", "-o", output.path, iconset.path]
    try process.run()
    process.waitUntilExit()
    guard process.terminationStatus == 0 else {
        throw IconError(message: "iconutil failed with status \(process.terminationStatus)")
    }
}

do {
    guard CommandLine.arguments.count == 3 else {
        throw IconError(message: "Usage: swift scripts/generate-workbench-icon.swift source.svg output.icns")
    }
    try generateIcon(source: CommandLine.arguments[1], destination: CommandLine.arguments[2])
} catch {
    FileHandle.standardError.write(Data("\(error.localizedDescription)\n".utf8))
    exit(1)
}
