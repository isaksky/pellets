class Pl < Formula
  desc "Local SQLite task queue for coding agents"
  homepage "https://github.com/isaksky/pellets"
  version "0.1.1"
  license "Apache-2.0"

  depends_on :macos

  if Hardware::CPU.arm?
    url "https://github.com/isaksky/pellets/releases/download/v0.1.1/pellets_0.1.1_darwin_arm64.tar.gz"
    sha256 "0f1421c8bb5cd84434016f34bc2218c211f4f4ab897bacf7ad27e0f169a759a1"
  else
    url "https://github.com/isaksky/pellets/releases/download/v0.1.1/pellets_0.1.1_darwin_amd64.tar.gz"
    sha256 "4f0d795409499c4bcb5bdfea20941fe6bec6b09e826918e1ef45f367b2748b12"
  end

  def install
    bin.install "pl"
  end

  test do
    assert_equal "pl #{version} (JSON schema 1)", shell_output("#{bin}/pl --version").strip
  end
end
