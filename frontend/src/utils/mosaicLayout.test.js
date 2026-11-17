import { describe, it, expect } from 'vitest';
import { mosaicGeometry, mosaicLayout, densityToExtent, extentToDensity, normalizeMosaicDensity, resolveMosaicItemRatio } from './mosaicLayout.js';

describe('single-column image stream', () => {
  it('uses indexed server ratios on first visit and lets decoded ratios correct them', () => {
    const item={displayAspectRatio:16/9};
    expect(resolveMosaicItemRatio(item,undefined)).toBe(16/9);
    expect(resolveMosaicItemRatio(item,4/3)).toBe(4/3);
    expect(resolveMosaicItemRatio({displayAspectRatio:-2},NaN)).toBe(1);
  });
  it('gives every file the full row width and its original height ratio', () => {
    for (const width of [316, 386, 764, 1436]) {
      const items = [16/9,9/16,1,0.05,15].map((ratio,index)=>({index,ratio}));
      const boxes = mosaicLayout(items,width);
      for (const item of items) {
        expect(parseFloat(boxes[item.index].width)).toBe(width);
        expect(parseFloat(boxes[item.index].height)).toBeCloseTo(width/item.ratio,8);
      }
    }
  });
  it('does not crop or clamp an extreme long screenshot or panorama', () => {
    const boxes=mosaicLayout([{index:8,ratio:0.05},{index:2,ratio:15}],390);
    expect(boxes[8]).toEqual({width:'390px',height:'7800px'});
    expect(boxes[2]).toEqual({width:'390px',height:'26px'});
  });
  it('uses a square placeholder for missing dimensions and rejects invalid width', () => {
    for(const ratio of [NaN,Infinity,0,-1,undefined]) {
      expect(mosaicLayout([{index:0,ratio}],390)[0]).toEqual({width:'390px',height:'390px'});
    }
    expect(mosaicLayout([],390)).toEqual({});
    for(const width of [0,-1,Infinity,NaN]) expect(mosaicLayout([{index:0,ratio:1}],width)).toEqual({});
  });
});

describe('proportional mosaic zoom', () => {
  it('packs ordinary images into rows without changing their ratio', () => {
    const items=[1,1.5,0.75,1,2,0.6,1.5,1].map((ratio,index)=>({index,ratio}));
    const boxes=mosaicLayout(items,390,{density:0.35});
    expect(Object.keys(boxes)).toHaveLength(items.length);
    expect(parseFloat(boxes[0].width)).toBeLessThan(390);
    for (const item of items) {
      const box=boxes[item.index];
      expect(parseFloat(box.width)/parseFloat(box.height)).toBeCloseTo(item.ratio,8);
      expect(parseFloat(box.width)).toBeGreaterThan(0);
      expect(parseFloat(box.width)).toBeLessThanOrEqual(390);
    }
  });
  it('forces every narrow portrait into its own full-width row at the maximum', () => {
    const items=Array.from({length:8},(_,index)=>({index,ratio:0.25}));
    for (const width of [316,390,844,1440]) {
      for (const box of Object.values(mosaicLayout(items,width,{density:1}))) {
        expect(box).toEqual({width:`${width}px`,height:`${width*4}px`});
      }
    }
  });
  it('keeps extreme ratios and missing metadata visible at every size', () => {
    const items=[0.05,20,NaN,Infinity,-1,0.5,2].map((ratio,index)=>({index,ratio}));
    for (const density of [0,0.25,0.5,0.75,0.999,1]) {
      const boxes=mosaicLayout(items,390,{density});
      for (const item of items) {
        const box=boxes[item.index];
        expect(Number.isFinite(parseFloat(box.width))).toBe(true);
        expect(parseFloat(box.height)).toBeGreaterThan(0);
        const ratio=Number.isFinite(item.ratio)&&item.ratio>0?item.ratio:1;
        expect(parseFloat(box.width)/parseFloat(box.height)).toBeCloseTo(ratio,8);
      }
    }
  });
});

 describe('density scale', () => {
  it('maps the pinch extent continuously and reversibly between both endpoints', () => {
    for (const width of [316,390,1440]) for (const density of [0,0.1,0.35,0.75,1]) {
      expect(extentToDensity(width,densityToExtent(width,density))).toBeCloseTo(density,10);
    }
    for (const value of [null,undefined,'','bad',Infinity]) expect(normalizeMosaicDensity(value)).toBe(1);
  });
});


describe('justified rows from screenshot feedback', () => {
  it('keeps accumulating a tall portrait row instead of shrinking it and reserving blank width', () => {
    const items=[0.5,0.5,0.5,2,1,1.5,1,2].map((ratio,index)=>({ratio,index}));
    const geometry=mosaicGeometry(items,390,{density:0.25});
    expect(geometry.rows[0].indices.length).toBeGreaterThan(3);
    for(const row of geometry.rows.slice(0,-1)) {
      expect(row.width).toBeCloseTo(390,8);
      expect(row.complete).toBe(true);
    }
  });
  it('has equal-height, full-width ordinary rows at all densities, allowing only the final row to be incomplete', () => {
    const ratios=[0.45,0.65,1,2.2,0.45,2.2,0.5,1.6,1,1,0.05,15];
    const items=Array.from({length:120},(_,index)=>({index,ratio:ratios[index%ratios.length]}));
    for(const width of [316,390,768,1440]) for(const density of [0,0.1,0.25,0.5,0.75,0.99,1]) {
      const geometry=mosaicGeometry(items,width,{density});
      const boxMap=new Map(Object.entries(geometry.boxes));
      for(const [index,row] of geometry.rows.entries()) {
        if(index<geometry.rows.length-1) expect(row.width).toBeCloseTo(width,7);
        for(const key of row.indices) {
          const box=boxMap.get(String(key));
          expect(parseFloat(box.height)).toBeCloseTo(row.height,8);
          expect(parseFloat(box.top)).toBeCloseTo(row.top,8);
          expect(parseFloat(box.width)/row.height).toBeCloseTo(items.at(key).ratio,8);
        }
      }
      expect(Object.keys(geometry.boxes)).toHaveLength(items.length);
    }
  });
  it('retains deterministic positions and total height for a single column', () => {
    const geometry=mosaicGeometry([{index:1,ratio:2},{index:2,ratio:0.5}],390,{density:1});
    expect(geometry.rows[1].top).toBe(203);
    expect(geometry.height).toBe(983);
    expect(geometry.boxes[2].insetInlineStart).toBe('2px');
  });
});
