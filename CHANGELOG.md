# Changelog

## [0.2.1](https://github.com/clappingmonkey/Deplexity/compare/v0.2.0...v0.2.1) (2026-05-30)


### Bug Fixes

* **ci:** add platforms dep to MODULE.bazel for cross-compilation ([8a8068e](https://github.com/clappingmonkey/Deplexity/commit/8a8068e2fc82c6e2fbbeefac70925ea5a217c6e0))

## [0.2.0](https://github.com/clappingmonkey/Deplexity/compare/v0.1.0...v0.2.0) (2026-05-30)


### Features

* add adaptive rate limiting, network retries, and duplicate detection ([e368595](https://github.com/clappingmonkey/Deplexity/commit/e3685951ecf39cb4b60a2715a3dc64e506939c41))
* add Bazel 9 build system, CLI entrypoint, and unit tests ([dde7d97](https://github.com/clappingmonkey/Deplexity/commit/dde7d9740b0a3dbee38a62e9c52afa2a312c8a80))
* add chrome TLS fingerprint, verbose mode, and thread pagination ([3277c72](https://github.com/clappingmonkey/Deplexity/commit/3277c729c0adcb2e05781b52a181f634b7ade67b))
* add progress output, Ctrl+C support, and elapsed time ([ac93457](https://github.com/clappingmonkey/Deplexity/commit/ac93457be3c10f3c53d4e0d042b2f1aa5d5dcb95))
* copy thread files into space folders during export ([787c68e](https://github.com/clappingmonkey/Deplexity/commit/787c68e0106d9f689689d0aab33d462f9d37d7e5))
* switch to list_ask_threads API and add spaces endpoint ([0d8f521](https://github.com/clappingmonkey/Deplexity/commit/0d8f5216e03e3fa8e56fdb08fddedaee6829b581))
* two-phase resumable export with cached thread index ([d7da37b](https://github.com/clappingmonkey/Deplexity/commit/d7da37bcac8428a27a5176f27f05d01681c786b5))


### Bug Fixes

* add cmd/deplexity to repo (was excluded by gitignore) ([8543180](https://github.com/clappingmonkey/Deplexity/commit/85431801b36df1c8e00b3a28da9568a7acf4b423))
* add retry with backoff for 429/502/503/504 and fix pagination limit ([8ee833b](https://github.com/clappingmonkey/Deplexity/commit/8ee833be6b01e0300828963543216071f55cd837))
* eliminate cookie validation warnings by setting raw Cookie header ([16f802c](https://github.com/clappingmonkey/Deplexity/commit/16f802cfa89b65e3ed2396c2e392904756c42dce))
* populate space thread_uuids and omit zero created_at ([3062972](https://github.com/clappingmonkey/Deplexity/commit/306297255cb27779e257b43adc1f075ad3e68522))
* resolve PDF hang on long URLs and spurious exit message ([7c74141](https://github.com/clappingmonkey/Deplexity/commit/7c74141ab4c44474e2e602e38fa7dd18787832b2))
* use pointer for Space.CreatedAt to properly omit zero time ([bf50ca9](https://github.com/clappingmonkey/Deplexity/commit/bf50ca9eecf2f89074b8cf5fde4532bf6743ae6f))


### Documentation

* add README for public OSS release ([34180fb](https://github.com/clappingmonkey/Deplexity/commit/34180fb55d54c8b6c0ba121f87236863934f8817))
* clarify Ctrl+C behavior and signal handling pattern ([f90b121](https://github.com/clappingmonkey/Deplexity/commit/f90b121061d24713d725f12622109fc8b381e254))
* update LICENSE copyright holder name ([9ca3273](https://github.com/clappingmonkey/Deplexity/commit/9ca3273bacb8895c02dcba4e7d6330ccd102ae50))
* update README and AGENTS with PDF source format and signal handling changes ([07759b7](https://github.com/clappingmonkey/Deplexity/commit/07759b7298a29b41eb8f6313be3979b20ae8b4ed))
* update README and AGENTS.md with current architecture ([0507eec](https://github.com/clappingmonkey/Deplexity/commit/0507eecd0d9c2483a3d15fd1860d229fee26aae6))
* update README and AGENTS.md with space folder structure ([8f721f6](https://github.com/clappingmonkey/Deplexity/commit/8f721f66defd7339109ba48b387c097dd6c780f8))
