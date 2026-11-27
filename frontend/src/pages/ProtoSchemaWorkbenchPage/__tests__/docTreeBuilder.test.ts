import { describe, it, expect } from 'vitest';
import type { ProtoDocTreeNodeDTO } from '../../../types';
import {
  buildDocPackageTree,
  buildDocSubsystemTree,
  findAncestorKeysForService,
} from '../docTreeBuilder';

const sampleTreeItems: ProtoDocTreeNodeDTO[] = [
  {
    node_key: 'sub:slug:sandbox-common',
    node_type: 'SUBSYSTEM',
    label: 'sandbox-common',
    has_source_info: true,
    children: [
      {
        node_key: 'schema:1',
        node_type: 'SCHEMA',
        label: 'sandbox-common-base (v2)',
        schema_id: 1,
        schema_name: 'sandbox-common-base',
        category: 'BASE_LIB',
        has_source_info: true,
        children: [
          {
            node_key: 'pkg:1:demo.payment',
            node_type: 'PACKAGE',
            label: 'demo.payment',
            has_source_info: true,
            children: [
              {
                node_key: 'svc:1:demo.payment.PaymentService',
                node_type: 'SERVICE',
                label: 'PaymentService',
                schema_id: 1,
                schema_name: 'sandbox-common-base',
                service_name: 'demo.payment.PaymentService',
                method_count: 2,
                has_source_info: true,
              },
            ],
          },
        ],
      },
      {
        node_key: 'schema:2',
        node_type: 'SCHEMA',
        label: 'empty-base-lib (v1)',
        schema_id: 2,
        schema_name: 'empty-base-lib',
        category: 'BASE_LIB',
        has_source_info: true,
        children: [
          {
            node_key: 'pkg:2:base',
            node_type: 'PACKAGE',
            label: 'base',
            has_source_info: true,
            children: [],
          },
        ],
      },
    ],
  },
  {
    node_key: 'sub:10',
    node_type: 'SUBSYSTEM',
    label: '晶圆运动控制',
    subsystem_id: 10,
    has_source_info: true,
    children: [
      {
        node_key: 'schema:3',
        node_type: 'SCHEMA',
        label: 'sandbox-fab-suite (v3)',
        schema_id: 3,
        schema_name: 'sandbox-fab-suite',
        category: 'BUSINESS',
        has_source_info: true,
        children: [
          {
            node_key: 'pkg:3:fab.optics',
            node_type: 'PACKAGE',
            label: 'fab.optics',
            has_source_info: true,
            children: [
              {
                node_key: 'svc:3:fab.optics.OpticsAlignService',
                node_type: 'SERVICE',
                label: 'OpticsAlignService',
                schema_id: 3,
                schema_name: 'sandbox-fab-suite',
                service_name: 'fab.optics.OpticsAlignService',
                method_count: 1,
                has_source_info: true,
              },
            ],
          },
          {
            node_key: 'pkg:3:fab.motion',
            node_type: 'PACKAGE',
            label: 'fab.motion',
            has_source_info: true,
            children: [
              {
                node_key: 'svc:3:fab.motion.StageMotionService',
                node_type: 'SERVICE',
                label: 'StageMotionService',
                schema_id: 3,
                schema_name: 'sandbox-fab-suite',
                service_name: 'fab.motion.StageMotionService',
                method_count: 4,
                is_deprecated: true,
                has_source_info: true,
              },
            ],
          },
        ],
      },
    ],
  },
];

describe('docTreeBuilder', () => {
  it('buildDocSubsystemTree filters empty schemas and compresses single-package schemas', () => {
    const subsystems = buildDocSubsystemTree(sampleTreeItems);
    expect(subsystems).toHaveLength(2);

    const commonSub = subsystems[0];
    expect(commonSub.label).toBe('sandbox-common');
    expect(commonSub.schemas).toHaveLength(1);
    expect(commonSub.schemas[0].label).toBe('sandbox-common-base (v2)');
    expect(commonSub.schemas[0].directServices).toHaveLength(1);
    expect(commonSub.schemas[0].singlePackageName).toBe('demo.payment');
    expect(commonSub.serviceCount).toBe(1);
    expect(commonSub.methodCount).toBe(2);

    const fabSub = subsystems[1];
    expect(fabSub.schemas[0].directServices).toBeNull();
    expect(fabSub.schemas[0].packages).toHaveLength(2);
    expect(fabSub.serviceCount).toBe(2);
    expect(fabSub.methodCount).toBe(5);
  });

  it('buildDocPackageTree aggregates services across subsystems sorted by package and service name', () => {
    const extraSamePkg: ProtoDocTreeNodeDTO = {
      node_key: 'sub:99',
      node_type: 'SUBSYSTEM',
      label: '其他域',
      has_source_info: true,
      children: [
        {
          node_key: 'schema:9',
          node_type: 'SCHEMA',
          label: 'extra-pay (v1)',
          schema_id: 9,
          schema_name: 'extra-pay',
          has_source_info: true,
          children: [
            {
              node_key: 'pkg:9:demo.payment',
              node_type: 'PACKAGE',
              label: 'demo.payment',
              has_source_info: true,
              children: [
                {
                  node_key: 'svc:9:demo.payment.RefundService',
                  node_type: 'SERVICE',
                  label: 'RefundService',
                  schema_id: 9,
                  service_name: 'demo.payment.RefundService',
                  method_count: 3,
                  has_source_info: true,
                },
                {
                  node_key: 'svc:1-dup:demo.payment.PaymentService',
                  node_type: 'SERVICE',
                  label: 'PaymentService',
                  schema_id: 1,
                  service_name: 'demo.payment.PaymentService',
                  method_count: 2,
                  has_source_info: true,
                },
              ],
            },
          ],
        },
      ],
    };

    const packages = buildDocPackageTree([...sampleTreeItems, extraSamePkg]);
    expect(packages.map((p) => p.packageName)).toEqual(['demo.payment', 'fab.motion', 'fab.optics']);
    expect(packages[0].services.map((s) => s.label)).toEqual(['PaymentService', 'RefundService']);
    expect(packages[0].serviceCount).toBe(2);
    expect(packages[0].methodCount).toBe(5);
  });

  it('findAncestorKeysForService returns subsystem/schema(/package) keys accurately', () => {
    const subsystems = buildDocSubsystemTree(sampleTreeItems);

    expect(
      findAncestorKeysForService(subsystems, 'demo.payment.PaymentService', 1)
    ).toEqual(['sub:slug:sandbox-common', 'schema:1']);

    expect(
      findAncestorKeysForService(subsystems, 'fab.motion.StageMotionService', 3)
    ).toEqual(['sub:10', 'schema:3', 'pkg:3:fab.motion']);

    expect(
      findAncestorKeysForService(subsystems, 'fab.motion.StageMotionService', 999)
    ).toEqual(['sub:10', 'schema:3', 'pkg:3:fab.motion']);

    expect(findAncestorKeysForService(subsystems, 'missing.Service')).toEqual([]);
  });
});
