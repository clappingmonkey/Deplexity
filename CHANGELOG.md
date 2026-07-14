# Changelog

## [0.2.4](https://github.com/clappingmonkey/Deplexity/compare/v0.2.3...v0.2.4) (2026-07-14)


### Bug Fixes

* **deps:** update module golang.org/x/net to v0.57.0 ([#38](https://github.com/clappingmonkey/Deplexity/issues/38)) ([fe0347b](https://github.com/clappingmonkey/Deplexity/commit/fe0347b2d441a518ff13f499c7760abec66dcc4a))

## [0.2.3](https://github.com/clappingmonkey/Deplexity/compare/v0.2.2...v0.2.3) (2026-07-02)


### Bug Fixes

* **deps:** update module github.com/schollz/progressbar/v3 to v3.19.1 ([#34](https://github.com/clappingmonkey/Deplexity/issues/34)) ([89d5f46](https://github.com/clappingmonkey/Deplexity/commit/89d5f464445cafb3f1c146c4b21799c390c54db3))
* **deps:** update module golang.org/x/net to v0.56.0 ([#27](https://github.com/clappingmonkey/Deplexity/issues/27)) ([17d5f79](https://github.com/clappingmonkey/Deplexity/commit/17d5f79696e589fd258e8eb44d2860c0f76ed216))


### Documentation

* add terminal demo GIF to README ([#35](https://github.com/clappingmonkey/Deplexity/issues/35)) ([1730e66](https://github.com/clappingmonkey/Deplexity/commit/1730e66ddd358743b978a1b594fff539416c309f))
* remove "(coming soon)" from releases link ([#36](https://github.com/clappingmonkey/Deplexity/issues/36)) ([63468a0](https://github.com/clappingmonkey/Deplexity/commit/63468a03dc6ee97db3c27d91c9c09af2694bf9f1))

## [0.2.2](https://github.com/clappingmonkey/Deplexity/compare/v0.2.1...v0.2.2) (2026-05-30)


### Bug Fixes

* **ci:** add --repo flag to gh commands in auto-approve workflow ([217db79](https://github.com/clappingmonkey/Deplexity/commit/217db799d0c1f15492fd748d1d560c19c81a4c75))
* **ci:** add RENOVATE_REPOSITORIES to renovate workflow ([50f0792](https://github.com/clappingmonkey/Deplexity/commit/50f07920fd8c3e122fcb71c60c434a0255be76f7))
* **ci:** allow bazel lockfile updates and add repo labels ([f6cffcd](https://github.com/clappingmonkey/Deplexity/commit/f6cffcd6f8de0f3dc1efe62c9aa5c2c863b955bf))
* **ci:** use PAT for auto-approve instead of GitHub App token ([fad7414](https://github.com/clappingmonkey/Deplexity/commit/fad74149d6b8aebf2d499b063f18dc4e0807b328))
* **deps:** update module github.com/gpdf-dev/gpdf to v1.0.11 ([#4](https://github.com/clappingmonkey/Deplexity/issues/4)) ([5be7a0f](https://github.com/clappingmonkey/Deplexity/commit/5be7a0fc8445b61524424f6f404a1fd514e2fadc))
* **deps:** update module golang.org/x/net to v0.55.0 ([#8](https://github.com/clappingmonkey/Deplexity/issues/8)) ([93e9f2d](https://github.com/clappingmonkey/Deplexity/commit/93e9f2deda1b5ed8ce2df8cef9a34d515b90aae8))

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
