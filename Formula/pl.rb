class Pl < Formula
  desc "Local SQLite task queue for coding agents"
  homepage "https://github.com/isaksky/pellets"
  license "Apache-2.0"

  depends_on :macos

  if Hardware::CPU.arm?
    url "https://github.com/isaksky/pellets/releases/download/v0.1.0/pellets_0.1.0_darwin_arm64.tar.gz"
    sha256 "2f25117b0412c176170e89203e659ae526c37a4b9f4f0561b0c6ecc410cc8fe6"
  else
    url "https://github.com/isaksky/pellets/releases/download/v0.1.0/pellets_0.1.0_darwin_amd64.tar.gz"
    sha256 "6128076256029d60a324f1d3f768e01bb3198bd49ecaa43b473e62fc78f010ab"
  end

  def install
    bin.install "pl"
  end

  test do
    assert_equal "pl #{version} (JSON schema 1)", shell_output("#{bin}/pl --version").strip
  end
end
