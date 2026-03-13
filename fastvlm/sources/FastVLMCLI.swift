import ArgumentParser
import CoreImage
import Foundation
import MLX
import MLXLMCommon
import MLXNN
import MLXVLM
import Tokenizers

@main
struct FastVLMCLI: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "fastvlm-cli",
        abstract: "Fast vision-language model inference on Apple Silicon"
    )

    @Argument(help: "Path to the image file")
    var imagePath: String

    @Option(name: .long, help: "Prompt for the model")
    var prompt: String = "Describe this image comprehensively for a coding AI agent that cannot see it. Include: 1) Full OCR of all visible text, preserving layout and indentation 2) Visual context: what the image shows (terminal, browser, UI, diagram, etc), layout, colors 3) Actionable details: error messages, file paths, line numbers, URLs, button labels, status indicators. Be thorough and preserve exact text."

    @Option(name: .long, help: "Maximum tokens to generate")
    var maxTokens: Int = 240

    @Option(name: .long, help: "Path to the model directory")
    var modelPath: String = {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        return "\(home)/ml-fastvlm/app/FastVLM/model"
    }()

    func run() async throws {
        let modelDir = URL(fileURLWithPath: modelPath)

        // Load configs
        let configData = try Data(contentsOf: modelDir.appendingPathComponent("config.json"))
        let config = try JSONDecoder().decode(FastVLMConfiguration.self, from: configData)

        let preprocData = try Data(contentsOf: modelDir.appendingPathComponent("preprocessor_config.json"))
        let preprocConfig = try JSONDecoder().decode(
            FastVLMPreProcessorConfiguration.self, from: preprocData)

        // Load tokenizer from model folder
        let tokenizer = try await AutoTokenizer.from(modelFolder: modelDir)

        // Create model and processor
        let model = FastVLMModel(config, modelDirectory: modelDir)
        let processor = FastVLMProcessor(preprocConfig, tokenizer: tokenizer)

        // Load weights
        let weightsURL = modelDir.appendingPathComponent("model.safetensors")
        let weights = try loadArrays(url: weightsURL)
        let sanitized = model.sanitize(weights: weights)
        try model.update(parameters: ModuleParameters.unflattened(sanitized), verify: .noUnusedKeys)
        eval(model)

        // Load image
        let imageURL = URL(fileURLWithPath: imagePath)
        guard let ciImage = CIImage(contentsOf: imageURL) else {
            throw ValidationError("Cannot load image: \(imagePath)")
        }

        // Prepare input
        let userInput = UserInput(prompt: .text(prompt), images: [.ciImage(ciImage)])
        let lmInput = try processor.prepare(input: userInput)

        // Generate using async stream
        let generateParameters = GenerateParameters(maxTokens: maxTokens, temperature: 0.0)
        let modelConfiguration = ModelConfiguration(directory: modelDir)
        let context = ModelContext(
            configuration: modelConfiguration,
            model: model,
            processor: processor,
            tokenizer: tokenizer
        )

        let stream = try MLXLMCommon.generate(
            input: lmInput,
            parameters: generateParameters,
            context: context
        )

        var output = ""
        for await generation in stream {
            switch generation {
            case .chunk(let text):
                output += text
            case .info:
                break
            case .toolCall:
                break
            @unknown default:
                break
            }
        }

        print(output)
    }
}
