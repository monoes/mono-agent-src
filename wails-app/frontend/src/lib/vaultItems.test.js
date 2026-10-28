import { describe, it, expect } from 'vitest'
import { isVaultImage, vaultFileKind, vaultKind } from './vaultItems.js'

describe('vaultItems', () => {
  it('tells images from the files the extension saves', () => {
    for (const f of ['img-001.png', 'img-002.JPG', 'img-003.jpeg', 'a.gif', 'a.webp', 'a.svg', 'a.bmp', 'a.ico']) {
      expect(isVaultImage({ filename: f })).toBe(true)
    }
    for (const f of ['img-004.pdf', 'img-005.md', 'img-006.txt', 'img-007.csv', 'noext', '']) {
      expect(isVaultImage({ filename: f })).toBe(false)
    }
    expect(isVaultImage(null)).toBe(false)
    expect(vaultFileKind({ filename: 'img-004.pdf' })).toBe('PDF')
    expect(vaultFileKind({ filename: 'noext' })).toBe('FILE')
  })

  it('tells audio and video from images and other files', () => {
    expect(vaultKind({ filename: 'img-001.mp3' })).toBe('audio')
    expect(vaultKind({ filename: 'img-002.MP4' })).toBe('video')
    expect(vaultKind({ filename: 'img-003.png' })).toBe('image')
    expect(vaultKind({ filename: 'img-004.pdf' })).toBe('')
    // The kind the CLI sends wins over the name.
    expect(vaultKind({ filename: 'x.bin', kind: 'video' })).toBe('video')
    // Audio and video are not images: no thumbnail, no image picker.
    expect(isVaultImage({ filename: 'img-001.mp3' })).toBe(false)
  })
})
