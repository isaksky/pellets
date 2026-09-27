class Pl < Formula
  desc "Local SQLite task queue for coding agents"
  homepage "https://github.com/isaksky/pellets"
  version "0.5.0"
  license "Apache-2.0"

  depends_on :macos

  if Hardware::CPU.arm?
    url "https://github.com/isaksky/pellets/releases/download/v0.5.0/pellets_0.5.0_darwin_arm64.tar.gz"
    sha256 "e7b5617894cfab03145cf2d61e8717be81c489726e910d1a768ed2815350612f"
  else
    url "https://github.com/isaksky/pellets/releases/download/v0.5.0/pellets_0.5.0_darwin_amd64.tar.gz"
    sha256 "8857bdb3861fbe46712bd081e491b914a8f17724e90f6ea7e124424cace78e2f"
  end

  def install
    bin.install "pl"
  end

  test do
    assert_equal "pl #{version} (JSON schema 1)", shell_output("#{bin}/pl --version").strip
  end
end
