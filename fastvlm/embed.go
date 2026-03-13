package fastvlm

import _ "embed"

//go:embed sources/Package.swift
var PackageSwift []byte

//go:embed sources/FastVLM.swift
var FastVLMSwift []byte

//go:embed sources/FastVLMCLI.swift
var FastVLMCLISwift []byte

//go:embed sources/MediaProcessingExtensions.swift
var MediaProcessingExtensionsSwift []byte
