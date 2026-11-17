import { beforeEach, describe, it, expect, vi } from 'vitest';

describe('client mosaic density', () => {
  beforeEach(() => { localStorage.clear(); vi.resetModules(); });
  it('starts at the existing single column, persists size and isolates accounts', async () => {
    const {getMosaicDensity:get,setMosaicDensity:set}=await import('./mosaicDensity.js');
    expect(get('alice')).toBe(1);
    set('alice',0.35);
    expect(get('alice')).toBe(0.35);
    expect(get('bob')).toBe(1);
    expect(localStorage.getItem('quantumMosaicDensity:v1:alice')).toBe('0.35');
  });
  it('clamps stored values and falls back for damaged preferences', async () => {
    localStorage.setItem('quantumMosaicDensity:v1:alice','not a number');
    const {getMosaicDensity:get,setMosaicDensity:set}=await import('./mosaicDensity.js');
    expect(get('alice')).toBe(1);
    set('alice',-9);expect(get('alice')).toBe(0);
    set('alice',5);expect(get('alice')).toBe(1);
  });
  it('snaps the final fraction to the same explicit endpoint shown by the control', async () => {
    const {getMosaicDensity:get,setMosaicDensity:set}=await import('./mosaicDensity.js');
    set('alice',0.999);expect(get('alice')).toBe(1);
    set('alice',0.33333);expect(get('alice')).toBe(0.33);
  });
  it('follows another tab and resets when storage is cleared', async () => {
    const {getMosaicDensity:get}=await import('./mosaicDensity.js');
    expect(get('alice')).toBe(1);
    window.dispatchEvent(new StorageEvent('storage',{key:'quantumMosaicDensity:v1:alice',newValue:'0.5'}));
    expect(get('alice')).toBe(0.5);
    window.dispatchEvent(new StorageEvent('storage',{key:null}));
    expect(get('alice')).toBe(1);
  });
});
