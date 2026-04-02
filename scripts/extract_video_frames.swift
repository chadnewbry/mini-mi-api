#!/usr/bin/env swift

import AVFoundation
import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

struct Options {
    let inputVideo: URL
    let outputDirectory: URL
    let framePrefix: String
    let frameCount: Int?
    let startFrame: Int?
    let endFrame: Int?
    let sampleFPS: Double
    let outputSize: Int
    let frameDurationMS: Int
    let gifFilename: String
}

enum ArgumentError: Error, LocalizedError {
    case missingValue(String)
    case unknownArgument(String)
    case invalidNumber(String, String)
    case missingRequired(String)

    var errorDescription: String? {
        switch self {
        case .missingValue(let flag):
            return "Missing value for \(flag)"
        case .unknownArgument(let flag):
            return "Unknown argument: \(flag)"
        case .invalidNumber(let flag, let value):
            return "Invalid numeric value for \(flag): \(value)"
        case .missingRequired(let flag):
            return "Missing required argument: \(flag)"
        }
    }
}

func parseArguments() throws -> Options {
    var inputVideo: URL?
    var outputDirectory: URL?
    var framePrefix = "frame"
    var frameCount: Int?
    var startFrame: Int?
    var endFrame: Int?
    var sampleFPS = 8.0
    var outputSize = 512
    var frameDurationMS = 90
    var gifFilename = "preview.gif"

    var index = 1
    while index < CommandLine.arguments.count {
        let argument = CommandLine.arguments[index]
        func nextValue() throws -> String {
            guard index + 1 < CommandLine.arguments.count else {
                throw ArgumentError.missingValue(argument)
            }
            index += 1
            return CommandLine.arguments[index]
        }

        switch argument {
        case "--input-video":
            inputVideo = URL(fileURLWithPath: try nextValue())
        case "--output-dir":
            outputDirectory = URL(fileURLWithPath: try nextValue(), isDirectory: true)
        case "--frame-prefix":
            framePrefix = try nextValue()
        case "--frame-count":
            let value = try nextValue()
            guard let parsed = Int(value) else {
                throw ArgumentError.invalidNumber(argument, value)
            }
            frameCount = parsed
        case "--start-frame":
            let value = try nextValue()
            guard let parsed = Int(value) else {
                throw ArgumentError.invalidNumber(argument, value)
            }
            startFrame = parsed
        case "--end-frame":
            let value = try nextValue()
            guard let parsed = Int(value) else {
                throw ArgumentError.invalidNumber(argument, value)
            }
            endFrame = parsed
        case "--sample-fps":
            let value = try nextValue()
            guard let parsed = Double(value) else {
                throw ArgumentError.invalidNumber(argument, value)
            }
            sampleFPS = parsed
        case "--output-size":
            let value = try nextValue()
            guard let parsed = Int(value) else {
                throw ArgumentError.invalidNumber(argument, value)
            }
            outputSize = parsed
        case "--frame-duration-ms":
            let value = try nextValue()
            guard let parsed = Int(value) else {
                throw ArgumentError.invalidNumber(argument, value)
            }
            frameDurationMS = parsed
        case "--gif-filename":
            gifFilename = try nextValue()
        default:
            throw ArgumentError.unknownArgument(argument)
        }

        index += 1
    }

    guard let inputVideo else {
        throw ArgumentError.missingRequired("--input-video")
    }
    guard let outputDirectory else {
        throw ArgumentError.missingRequired("--output-dir")
    }
    return Options(
        inputVideo: inputVideo,
        outputDirectory: outputDirectory,
        framePrefix: framePrefix,
        frameCount: frameCount,
        startFrame: startFrame,
        endFrame: endFrame,
        sampleFPS: sampleFPS,
        outputSize: outputSize,
        frameDurationMS: frameDurationMS,
        gifFilename: gifFilename
    )
}

func resizedImage(from cgImage: CGImage, size: Int) -> CGImage? {
    guard
        let colorSpace = cgImage.colorSpace ?? CGColorSpace(name: CGColorSpace.sRGB),
        let context = CGContext(
            data: nil,
            width: size,
            height: size,
            bitsPerComponent: 8,
            bytesPerRow: 0,
            space: colorSpace,
            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue
        )
    else {
        return nil
    }

    context.interpolationQuality = .high
    context.draw(cgImage, in: CGRect(x: 0, y: 0, width: size, height: size))
    return context.makeImage()
}

func pngData(for image: CGImage) -> Data? {
    let mutableData = NSMutableData()
    guard let destination = CGImageDestinationCreateWithData(
        mutableData,
        UTType.png.identifier as CFString,
        1,
        nil
    ) else {
        return nil
    }
    CGImageDestinationAddImage(destination, image, nil)
    guard CGImageDestinationFinalize(destination) else {
        return nil
    }
    return mutableData as Data
}

func extractFrames(options: Options) throws -> [URL] {
    let asset = AVURLAsset(url: options.inputVideo)
    let durationSeconds = CMTimeGetSeconds(asset.duration)
    guard durationSeconds.isFinite, durationSeconds > 0 else {
        throw NSError(domain: "extract_video_frames", code: 1, userInfo: [
            NSLocalizedDescriptionKey: "Could not determine video duration."
        ])
    }
    let resolvedFrameCount = options.frameCount ?? max(2, Int(round(durationSeconds * options.sampleFPS)))
    let resolvedStartFrame = max(1, min(options.startFrame ?? 1, resolvedFrameCount))
    let resolvedEndFrame = max(resolvedStartFrame, min(options.endFrame ?? resolvedFrameCount, resolvedFrameCount))

    try FileManager.default.createDirectory(
        at: options.outputDirectory,
        withIntermediateDirectories: true
    )
    let existingFrameURLs = try FileManager.default.contentsOfDirectory(
        at: options.outputDirectory,
        includingPropertiesForKeys: nil,
        options: [.skipsHiddenFiles]
    )
    for existingFrameURL in existingFrameURLs where existingFrameURL.lastPathComponent.hasPrefix("\(options.framePrefix)_") && existingFrameURL.pathExtension.lowercased() == "png" {
        try? FileManager.default.removeItem(at: existingFrameURL)
    }

    let generator = AVAssetImageGenerator(asset: asset)
    generator.appliesPreferredTrackTransform = true
    generator.apertureMode = .encodedPixels
    generator.requestedTimeToleranceBefore = .zero
    generator.requestedTimeToleranceAfter = .zero

    var frameURLs: [URL] = []
    let denominator = max(1, resolvedFrameCount - 1)
    for sampleFrameNumber in resolvedStartFrame...resolvedEndFrame {
        let sampleIndex = sampleFrameNumber - 1
        let progress = Double(sampleIndex) / Double(denominator)
        let seconds = durationSeconds * progress
        let time = CMTime(seconds: seconds, preferredTimescale: 600)
        let cgImage = try generator.copyCGImage(at: time, actualTime: nil)
        guard let resized = resizedImage(from: cgImage, size: options.outputSize),
              let data = pngData(for: resized) else {
            throw NSError(domain: "extract_video_frames", code: 2, userInfo: [
                NSLocalizedDescriptionKey: "Could not encode frame \(sampleFrameNumber)."
            ])
        }

        let exportedIndex = frameURLs.count + 1
        let frameName = String(format: "%@_%02d.png", options.framePrefix, exportedIndex)
        let frameURL = options.outputDirectory.appendingPathComponent(frameName)
        try data.write(to: frameURL, options: .atomic)
        frameURLs.append(frameURL)
    }

    return frameURLs
}

func writeGIF(frameURLs: [URL], options: Options) throws -> URL {
    guard !frameURLs.isEmpty else {
        throw NSError(domain: "extract_video_frames", code: 5, userInfo: [
            NSLocalizedDescriptionKey: "No frames matched the selected range."
        ])
    }

    let destinationURL = options.outputDirectory.appendingPathComponent(options.gifFilename)
    guard let destination = CGImageDestinationCreateWithURL(
        destinationURL as CFURL,
        UTType.gif.identifier as CFString,
        frameURLs.count,
        nil
    ) else {
        throw NSError(domain: "extract_video_frames", code: 3, userInfo: [
            NSLocalizedDescriptionKey: "Could not create GIF destination."
        ])
    }

    let gifProperties: [CFString: Any] = [
        kCGImagePropertyGIFDictionary: [
            kCGImagePropertyGIFLoopCount: 0
        ]
    ]
    CGImageDestinationSetProperties(destination, gifProperties as CFDictionary)

    for frameURL in frameURLs {
        guard
            let source = CGImageSourceCreateWithURL(frameURL as CFURL, nil),
            let cgImage = CGImageSourceCreateImageAtIndex(source, 0, nil)
        else {
            continue
        }
        let frameProperties: [CFString: Any] = [
            kCGImagePropertyGIFDictionary: [
                kCGImagePropertyGIFDelayTime: Double(options.frameDurationMS) / 1000.0
            ]
        ]
        CGImageDestinationAddImage(destination, cgImage, frameProperties as CFDictionary)
    }

    guard CGImageDestinationFinalize(destination) else {
        throw NSError(domain: "extract_video_frames", code: 4, userInfo: [
            NSLocalizedDescriptionKey: "Could not finalize GIF."
        ])
    }
    return destinationURL
}

do {
    let options = try parseArguments()
    let frameURLs = try extractFrames(options: options)
    let gifURL = try writeGIF(frameURLs: frameURLs, options: options)

    print("Sample FPS: \(options.sampleFPS)")
    print("Resolved frame count: \(frameURLs.count)")
    if options.startFrame != nil || options.endFrame != nil {
        let requestedFrameCount = options.frameCount ?? frameURLs.count
        let start = max(1, min(options.startFrame ?? 1, requestedFrameCount))
        let end = max(start, min(options.endFrame ?? requestedFrameCount, requestedFrameCount))
        print("GIF frame range: \(start)-\(end)")
    }
    print("Extracted frames:")
    for frameURL in frameURLs {
        print("- \(frameURL.path)")
    }
    print("Preview GIF: \(gifURL.path)")
} catch {
    fputs("\(error.localizedDescription)\n", stderr)
    exit(1)
}
