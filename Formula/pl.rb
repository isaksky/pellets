class Pl < Formula
  desc "Local SQLite task queue for coding agents"
  homepage "https://github.com/isaksky/pellets"
  version "0.3.0"
  license "Apache-2.0"

  depends_on :macos

  if Hardware::CPU.arm?
    url "https://github.com/isaksky/pellets/releases/download/v0.3.0/pellets_0.3.0_darwin_arm64.tar.gz"
    sha256 "5dfd4de21bb1d2902fbc38190f9220ecb620ad80602b8256d6813aeb71242a87"
  else
    url "https://github.com/isaksky/pellets/releases/download/v0.3.0/pellets_0.3.0_darwin_amd64.tar.gz"
    sha256 "d68a5d507a2a563c322fee2ae393c990eed8da3ec566ac9db758e2858e1c9d7e"
  end

  def install
    bin.install "pl"
  end

  test do
    assert_equal "pl #{version} (JSON schema 1)", shell_output("#{bin}/pl --version").strip
  end
end
