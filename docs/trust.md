# Trust and Rebuilds

Rebuilding is the semantic reproduction of a previously-built artifact through a
comparable yet distinct process. Rebuilding is primarily a means of gaining
confidence in the integrity of a build. However, it is a signal of integrity,
not a guarantee: Rebuilding cannot solve all open source security issues but
should instead be thought of as a (valuable!) tool in the security toolbox.

## The Basics

At a high level, a **build** encompasses the production of a software artifact
using explicit and environmental inputs. Using
[SLSA's terminology](https://slsa.dev/spec/v1.0/terminology), these elements
include the builder (even its OS, kernel, and physical infrastructure), build
script, any external dependencies, and the source code. Collectively, these can
be considered **build factors**, and we can view a build as the functional
result of this input set.

When we attempt to **rebuild** a package, we are essentially trying to find a
set of build factors that produces an identical artifact to the original.
However, it's important to note that some factors from the original build might
not be essential for reproducing the artifact, while other factors might
naturally change between the original and the rebuild (e.g., timestamps, URLs).
This inherent variability in build factors is a key consideration when assessing
the reliability and security of rebuilt artifacts.

## Interpreting Results

### What does a successful rebuild tell us?

A successful rebuild serves as a mild positive signal that a build was free from
tampering, demonstrating both the reproducibility of the artifact and the
accuracy of the rebuild definition. If any sources of compromise were present in
the original package's content, the same (or a meaningfully similar) compromise
must also exist within the rebuild's set of build factors.

Notably, a successful rebuild cannot necessarily rule out all sources of
compromise, such as an attacker with control of the source repository itself.
However, in cases like
[the xz attack](https://research.swtch.com/xz-timeline#:~:text=malicious%20build%2Dto%2Dhost.m4),
the backdoored source archive distributed downstream would not have been rebuilt
successfully, as additional malicious content was introduced during the archive
build process. This highlights the potential for rebuilds to detect malicious
modifications and, in so doing, force attackers into more transparent avenues of
attack.

### What does an unsuccessful rebuild tell us?

An unsuccessful rebuild should be interpreted carefully and in context. Notably,
there are many reasons why a rebuild might fail that are not indicative of an
attack:

*   *Automation failure*: The automation process may have failed to identify an
    accurate build script or configuration.
*   *Metadata discrepancy*: A published artifact may correspond to a repository
    state near to the documented release point in the source repository (e.g.
    the version's git tag).
*   *Environmental differences*: Variations in the build environment, such as
    different versions of dependencies or tools, can lead to inconsistencies
    despite attempts to adhere to the original build.
*   *Non-deterministic builds*: Some builds are inherently non-deterministic,
    meaning they can produce slightly different results even with the same
    inputs.

It's important to recognize that not all failures are equal in severity, and
many are unrelated to the package owner's actions. Still, rebuild failures are
actionable and can often be addressed in multiple ways. They should be treated
more as potential opportunities to improve build reproducibility and
transparency rather than unconditional indicators of maintainer malice or
carelessness.
