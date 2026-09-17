class Pl < Formula
  desc "Local SQLite task queue for coding agents"
  homepage "https://github.com/isaksky/pellets"
  version "0.3.0"
  license "Apache-2.0"

  depends_on :macos

  if Hardware::CPU.arm?
    url "https://github.com/isaksky/pellets/releases/download/v0.3.0/pellets_0.3.0_darwin_arm64.tar.gz"
    sha256 "c1c66b94e667bc12b76b7471e471aec5287b13d1da99fce443081b64a09c908e"
  else
    url "https://github.com/isaksky/pellets/releases/download/v0.3.0/pellets_0.3.0_darwin_amd64.tar.gz"
    sha256 "2970ca95650d2e094d6568883c9dfb0e4d5ec18599846367adc3506ee1e261d1"
  end

  def install
    bin.install "pl"
  end

  test do
    assert_equal "pl #{version} (JSON schema 1)", shell_output("#{bin}/pl --version").strip
  end
end
